package capabilities

import (
	"archive/zip"
	"bytes"
	"encoding/hex"
	"encoding/xml"
	"io"
	"path"
	"strings"

	"xirang/backend/internal/backupasset/processing/capabilityspec"
)

const (
	documentPackageMaxEntries       = 10_000
	documentPackageMaxEntryBytes    = 64 << 20
	documentPackageMaxExpandedBytes = 256 << 20
	documentPackageMaxRatio         = 100
	documentXMLMaxBytes             = 1 << 20
	documentXMLMaxTokens            = 100_000
	documentXMLTotalMaxBytes        = 8 << 20
	documentXMLTotalMaxTokens       = 500_000
)

type DocumentPlan struct {
	ExecutableID ExecutableID
	ArgProfile   ToolArgProfile
	Warnings     []string
}

func PlanDocument(input []byte, mediaType string) (DocumentPlan, error) {
	switch mediaType {
	case "application/pdf":
		if len(input) < 16 || !bytes.HasPrefix(input, []byte("%PDF-")) || !bytes.Contains(input[max(0, len(input)-1024):], []byte("%%EOF")) {
			return DocumentPlan{}, ErrInvalidToolOutput
		}
		if containsActivePDFAction(input) {
			return DocumentPlan{}, capabilityspec.ErrUnsupportedMedia
		}
		return DocumentPlan{ExecutableID: ExecutablePDFToCairo, ArgProfile: ArgsPDFPages}, nil
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"application/vnd.openxmlformats-officedocument.presentationml.presentation":
		if err := inspectDocumentPackage(input, mediaType, false); err != nil {
			return DocumentPlan{}, err
		}
		return DocumentPlan{ExecutableID: ExecutableLibreOffice, ArgProfile: ArgsOfficePDF}, nil
	case "application/vnd.oasis.opendocument.text", "application/vnd.oasis.opendocument.spreadsheet", "application/vnd.oasis.opendocument.presentation":
		if err := inspectDocumentPackage(input, mediaType, true); err != nil {
			return DocumentPlan{}, err
		}
		return DocumentPlan{ExecutableID: ExecutableLibreOffice, ArgProfile: ArgsOfficePDF}, nil
	default:
		return DocumentPlan{}, capabilityspec.ErrUnsupportedMedia
	}
}

func containsActivePDFAction(input []byte) bool {
	active := map[string]struct{}{
		"JavaScript": {}, "JS": {}, "Launch": {}, "URI": {}, "GoToR": {}, "GoToE": {},
		"SubmitForm": {}, "ImportData": {}, "OpenAction": {}, "AA": {}, "Encrypt": {},
		"ObjStm": {}, "XRef": {}, "EmbeddedFile": {}, "Filespec": {}, "AcroForm": {},
		"RichMedia": {}, "Rendition": {}, "Movie": {}, "Sound": {},
	}
	for index := 0; index < len(input); index++ {
		if input[index] != '/' {
			continue
		}
		name := make([]byte, 0, 32)
		for cursor := index + 1; cursor < len(input) && !pdfNameDelimiter(input[cursor]); {
			if len(name) >= 64 {
				break
			}
			if input[cursor] == '#' && cursor+2 < len(input) {
				decoded := []byte{0}
				if _, err := hex.Decode(decoded, input[cursor+1:cursor+3]); err == nil {
					name = append(name, decoded[0])
					cursor += 3
					continue
				}
			}
			name = append(name, input[cursor])
			cursor++
		}
		if _, blocked := active[string(name)]; blocked {
			return true
		}
	}
	return false
}

func pdfNameDelimiter(value byte) bool {
	switch value {
	case 0, '\t', '\n', '\f', '\r', ' ', '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	default:
		return false
	}
}

func inspectDocumentPackage(input []byte, mediaType string, odf bool) error {
	if len(input) < 4 || len(input) > documentPackageMaxExpandedBytes || !bytes.Equal(input[:2], []byte{'P', 'K'}) {
		return ErrInvalidToolOutput
	}
	reader, err := zip.NewReader(bytes.NewReader(input), int64(len(input)))
	if err != nil || len(reader.File) == 0 || len(reader.File) > documentPackageMaxEntries {
		return ErrInvalidToolOutput
	}
	members := make(map[string]bool, len(reader.File))
	lookup := make(map[string]*zip.File, len(reader.File))
	var expanded uint64
	for _, file := range reader.File {
		clean, pathErr := validateArchivePath(file.Name, 32)
		lower := strings.ToLower(clean)
		mode := file.FileInfo().Mode()
		if pathErr != nil || members[lower] || file.Flags&1 != 0 || mode.IsDir() || !mode.IsRegular() ||
			mode&(0o7000) != 0 || file.UncompressedSize64 > documentPackageMaxEntryBytes ||
			file.UncompressedSize64 > uint64(documentPackageMaxExpandedBytes)-expanded ||
			(file.CompressedSize64 == 0 && file.UncompressedSize64 > 0) ||
			(file.CompressedSize64 > 0 && file.UncompressedSize64 > file.CompressedSize64*documentPackageMaxRatio) {
			return ErrInvalidToolOutput
		}
		if activeDocumentMember(lower, odf) {
			return capabilityspec.ErrUnsupportedMedia
		}
		expanded += file.UncompressedSize64
		members[lower] = true
		lookup[lower] = file
	}
	if odf {
		if err := validateODFMimetype(reader.File, lookup, mediaType); err != nil {
			return err
		}
	} else if !members["[content_types].xml"] {
		return ErrInvalidToolOutput
	}
	var scannedXMLBytes int64
	var scannedXMLTokens int
	for name, file := range lookup {
		if !allowlistedDocumentXML(name, odf) {
			continue
		}
		remainingBytes := int64(documentXMLTotalMaxBytes) - scannedXMLBytes
		remainingTokens := documentXMLTotalMaxTokens - scannedXMLTokens
		if remainingBytes <= 0 || remainingTokens <= 0 || file.UncompressedSize64 > uint64(remainingBytes) {
			return ErrInvalidToolOutput
		}
		payload, err := readBoundedDocumentPart(file, min(int64(documentXMLMaxBytes), remainingBytes))
		if err != nil {
			return err
		}
		tokens, err := inspectDocumentXML(payload, name, odf, members, min(documentXMLMaxTokens, remainingTokens))
		if err != nil {
			return err
		}
		scannedXMLBytes += int64(len(payload))
		scannedXMLTokens += tokens
	}
	return nil
}

func activeDocumentMember(name string, odf bool) bool {
	if strings.Contains(name, "vbaproject") || strings.HasSuffix(name, ".bin") || strings.HasSuffix(name, ".exe") ||
		strings.HasSuffix(name, ".dll") || strings.HasSuffix(name, ".com") {
		return true
	}
	if !odf {
		return false
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == "scripts" || segment == "basic" {
			return true
		}
	}
	return false
}

func validateODFMimetype(files []*zip.File, lookup map[string]*zip.File, mediaType string) error {
	file := lookup["mimetype"]
	if file == nil || len(files) == 0 || files[0] != file || file.Name != "mimetype" || file.Method != zip.Store ||
		file.UncompressedSize64 != uint64(len(mediaType)) || file.CompressedSize64 != file.UncompressedSize64 {
		return ErrInvalidToolOutput
	}
	payload, err := readBoundedDocumentPart(file, 128)
	if err != nil || string(payload) != mediaType {
		return ErrInvalidToolOutput
	}
	return nil
}

func allowlistedDocumentXML(name string, odf bool) bool {
	if odf {
		return strings.HasSuffix(name, ".xml")
	}
	return strings.HasSuffix(name, ".xml") || strings.HasSuffix(name, ".rels")
}

func readBoundedDocumentPart(file *zip.File, maximum int64) ([]byte, error) {
	if file == nil || file.UncompressedSize64 > uint64(maximum) {
		return nil, ErrInvalidToolOutput
	}
	part, err := file.Open()
	if err != nil {
		return nil, ErrInvalidToolOutput
	}
	payload, readErr := io.ReadAll(io.LimitReader(part, maximum+1))
	closeErr := part.Close()
	if readErr != nil || closeErr != nil || int64(len(payload)) > maximum || uint64(len(payload)) != file.UncompressedSize64 {
		return nil, ErrInvalidToolOutput
	}
	return payload, nil
}

func inspectDocumentXML(payload []byte, memberName string, odf bool, members map[string]bool, maximumTokens int) (int, error) {
	if maximumTokens <= 0 || maximumTokens > documentXMLMaxTokens {
		return 0, ErrInvalidToolOutput
	}
	decoder := xml.NewDecoder(bytes.NewReader(payload))
	decoder.Strict = true
	tokens := 0
	for {
		if tokens >= maximumTokens {
			return tokens, ErrInvalidToolOutput
		}
		token, err := decoder.Token()
		if err == io.EOF {
			return tokens, nil
		}
		if err != nil {
			return tokens, ErrInvalidToolOutput
		}
		tokens++
		switch value := token.(type) {
		case xml.Directive:
			return tokens, capabilityspec.ErrUnsupportedMedia
		case xml.ProcInst:
			if !canonicalXMLDeclaration(value, tokens) {
				return tokens, capabilityspec.ErrUnsupportedMedia
			}
			continue
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		local := strings.ToLower(start.Name.Local)
		space := strings.ToLower(start.Name.Space)
		if odf && (strings.Contains(local, "script") || strings.Contains(local, "macro") || strings.Contains(space, "script")) {
			return tokens, capabilityspec.ErrUnsupportedMedia
		}
		for _, attribute := range start.Attr {
			name := strings.ToLower(attribute.Name.Local)
			namespace := strings.ToLower(attribute.Name.Space)
			value := strings.TrimSpace(attribute.Value)
			lowerValue := strings.ToLower(value)
			if name == "targetmode" && strings.EqualFold(value, "external") {
				return tokens, capabilityspec.ErrUnsupportedMedia
			}
			if strings.Contains(name, "contenttype") || name == "media-type" || name == "full-path" {
				if strings.Contains(lowerValue, "macro") || strings.Contains(lowerValue, "script") ||
					strings.Contains(lowerValue, "vbaproject") || strings.HasPrefix(lowerValue, "basic/") ||
					strings.HasPrefix(lowerValue, "scripts/") {
					return tokens, capabilityspec.ErrUnsupportedMedia
				}
			}
			if odf && (name == "href" || strings.Contains(namespace, "xlink")) && externalDocumentReference(value, memberName, members) {
				return tokens, capabilityspec.ErrUnsupportedMedia
			}
		}
	}
}

func canonicalXMLDeclaration(instruction xml.ProcInst, tokenPosition int) bool {
	if tokenPosition != 1 || instruction.Target != "xml" {
		return false
	}
	switch string(instruction.Inst) {
	case `version="1.0"`,
		`version="1.0" encoding="UTF-8"`,
		`version="1.0" encoding="UTF-8" standalone="yes"`,
		`version="1.0" encoding="UTF-8" standalone="no"`:
		return true
	default:
		return false
	}
}

func externalDocumentReference(value, memberName string, members map[string]bool) bool {
	if value == "" || strings.HasPrefix(value, "#") {
		return false
	}
	target := value
	if index := strings.IndexByte(target, '#'); index >= 0 {
		target = target[:index]
	}
	if target == "" || strings.ContainsAny(target, "\x00\r\n\\") || strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") || strings.Contains(target, ":") {
		return true
	}
	clean := path.Clean(target)
	if clean != target || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return true
	}
	resolved := path.Join(path.Dir(memberName), clean)
	if resolved == "." || resolved == ".." || strings.HasPrefix(resolved, "../") {
		return true
	}
	return !members[strings.ToLower(resolved)]
}
