package processing

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"unicode/utf8"
)

const workKeyDomain = "xirang.backup_asset.work.v1"

func ComputeWorkKey(value WorkDescriptorV1) (string, error) {
	if err := ValidateWorkDescriptorV1(value); err != nil {
		return "", err
	}
	digest := sha256.New()
	encoder := canonicalEncoder{writer: digest}
	encoder.string(workKeyDomain)
	encoder.int64(int64(value.SchemaVersion))
	encoder.string(value.Source.RecoveryPointID)
	encoder.string(value.Source.EntryID)
	encoder.string(value.CatalogGenerationID)
	encoder.string(value.SourceFingerprint)
	encoder.string(value.EntryFingerprint)
	encoder.int64(value.ProviderCapabilityRevision)
	encoder.string(value.Capability)
	encoder.string(value.CapabilitySchema)
	encoder.string(value.PipelineFingerprint)
	encoder.string(value.OutputProfile)
	encoder.string(value.SecurityPolicyRevision)
	parameters := value.Parameters
	encoder.int64(int64(parameters.SchemaVersion))
	encoder.int64(int64(parameters.Width))
	encoder.int64(int64(parameters.Height))
	encoder.string(parameters.Codec)
	encoder.int64(int64(parameters.PageStart))
	encoder.int64(int64(parameters.PageEnd))
	encoder.int64(int64(parameters.Quality))
	encoder.string(parameters.Language)
	encoder.string(parameters.Model)
	encoder.string(parameters.FontProfile)
	encoder.int64(int64(parameters.MemberStart))
	encoder.int64(int64(parameters.MemberEnd))
	encoder.int64(parameters.FrameStart)
	encoder.int64(parameters.FrameEnd)
	encoder.int64(parameters.TimeStartMillis)
	encoder.int64(parameters.TimeEndMillis)
	encoder.string(parameters.Orientation)
	encoder.int64(int64(parameters.CropX))
	encoder.int64(int64(parameters.CropY))
	encoder.int64(int64(parameters.CropWidth))
	encoder.int64(int64(parameters.CropHeight))
	encoder.int64(parameters.MaxPages)
	encoder.int64(parameters.MaxPixels)
	encoder.int64(parameters.MaxDurationMillis)
	encoder.int64(parameters.MaxExpandedBytes)
	encoder.int64(parameters.MaxOutputBytes)
	encoder.int64(int64(parameters.MaxOutputCount))
	encoder.string(parameters.TruncationPolicy)
	encoder.boolean(parameters.RequiresMaterialization)
	if encoder.err != nil {
		return "", fmt.Errorf("%w: canonical encoding: %v", ErrInvalidContract, encoder.err)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

type canonicalEncoder struct {
	writer hash.Hash
	err    error
}

func (encoder *canonicalEncoder) string(value string) {
	if encoder.err != nil {
		return
	}
	if len(value) > int(^uint32(0)) {
		encoder.err = fmt.Errorf("value too large")
		return
	}
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	if _, err := encoder.writer.Write(length[:]); err != nil {
		encoder.err = err
		return
	}
	_, encoder.err = encoder.writer.Write([]byte(value))
}

func (encoder *canonicalEncoder) int64(value int64) {
	if encoder.err != nil {
		return
	}
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(value))
	_, encoder.err = encoder.writer.Write(encoded[:])
}

func (encoder *canonicalEncoder) boolean(value bool) {
	if value {
		encoder.int64(1)
		return
	}
	encoder.int64(0)
}

func DecodeWorkDescriptorV1(payload []byte) (WorkDescriptorV1, error) {
	if len(payload) == 0 || len(payload) > 65536 || !utf8.Valid(payload) || !json.Valid(payload) {
		return WorkDescriptorV1{}, fmt.Errorf("%w: invalid descriptor JSON", ErrInvalidContract)
	}
	if err := rejectDuplicateJSONMembers(payload); err != nil {
		return WorkDescriptorV1{}, err
	}
	if err := requireDescriptorFields(payload); err != nil {
		return WorkDescriptorV1{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var value WorkDescriptorV1
	if err := decoder.Decode(&value); err != nil {
		return WorkDescriptorV1{}, fmt.Errorf("%w: decode descriptor", ErrInvalidContract)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return WorkDescriptorV1{}, err
	}
	if err := ValidateWorkDescriptorV1(value); err != nil {
		return WorkDescriptorV1{}, err
	}
	return value, nil
}

func rejectDuplicateJSONMembers(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := scanJSONValue(decoder); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidContract, err)
	}
	return ensureJSONEOF(decoder)
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("object member is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate JSON member %q", key)
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return fmt.Errorf("unterminated JSON object")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return fmt.Errorf("unterminated JSON array")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter")
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("%w: trailing JSON value", ErrInvalidContract)
		}
		return fmt.Errorf("%w: trailing JSON: %v", ErrInvalidContract, err)
	}
	return nil
}

func requireDescriptorFields(payload []byte) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(payload, &root); err != nil {
		return fmt.Errorf("%w: descriptor object", ErrInvalidContract)
	}
	if err := requireExactFields(root, []string{
		"schema_version", "source", "catalog_generation_id", "source_fingerprint", "entry_fingerprint",
		"provider_capability_revision", "capability", "capability_schema", "pipeline_fingerprint",
		"output_profile", "security_policy_revision", "parameters",
	}); err != nil {
		return err
	}
	var source map[string]json.RawMessage
	if err := json.Unmarshal(root["source"], &source); err != nil {
		return fmt.Errorf("%w: source object", ErrInvalidContract)
	}
	if err := requireExactFields(source, []string{"recovery_point_id", "entry_id"}); err != nil {
		return err
	}
	var parameters map[string]json.RawMessage
	if err := json.Unmarshal(root["parameters"], &parameters); err != nil {
		return fmt.Errorf("%w: parameters object", ErrInvalidContract)
	}
	return requireExactFields(parameters, []string{
		"schema_version", "width", "height", "codec", "page_start", "page_end", "quality", "language",
		"model", "font_profile", "member_start", "member_end", "frame_start", "frame_end", "time_start_millis",
		"time_end_millis", "orientation", "crop_x", "crop_y", "crop_width", "crop_height", "max_pages",
		"max_pixels", "max_duration_millis", "max_expanded_bytes", "max_output_bytes", "max_output_count",
		"truncation_policy", "requires_materialization",
	})
}

func requireExactFields(value map[string]json.RawMessage, fields []string) error {
	if len(value) != len(fields) {
		return fmt.Errorf("%w: missing or unknown required field", ErrInvalidContract)
	}
	for _, field := range fields {
		if _, ok := value[field]; !ok {
			return fmt.Errorf("%w: required field %s is missing", ErrInvalidContract, field)
		}
	}
	return nil
}
