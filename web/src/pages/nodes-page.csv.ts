import type { TFunction } from "i18next";
import type { NewNodeInput, NodeRecord } from "@/types/domain";

export type CSVNodeRow = {
  name: string;
  host: string;
  username: string;
  port: number;
  tags: string;
};

/**
 * Line-aware import diagnostic. `line` is the 1-based physical line where the
 * record (or, for an unclosed quote, the quoted field) starts.
 * `unclosedQuote` and `strayQuote` are structural: they reject the whole file.
 */
export type CSVNodeRowError =
  | { line: number; code: "unclosedQuote" | "strayQuote" | "missingRequired" }
  | { line: number; code: "invalidPort"; port: string }
  | { line: number; code: "controlCharacter"; field: "username" };

export type CSVNodeParseResult = {
  rows: CSVNodeRow[];
  errors: CSVNodeRowError[];
};

// Spreadsheet formula triggers, also after leading whitespace or control
// characters that spreadsheets may strip.
// eslint-disable-next-line no-control-regex -- intentional: detect spreadsheet formula injection via control chars
const FORMULA_PREFIX = /^[\s\x00-\x1f]*[=+\-@\t\r]/;
// C0/C1 controls, DEL and Unicode line/paragraph separators; trim() would hide several of them.
// eslint-disable-next-line no-control-regex -- intentional: reject line breaks and control characters
const CONTROL_CHARACTER = /[\x00-\x1f\x7f-\x9f\u2028\u2029]/;
const DECIMAL_PORT = /^[0-9]+$/;
const DEFAULT_PORT = 22;
const DEFAULT_USERNAME = "root";

const EXPORT_FIELDS = ["name", "host", "username", "port", "tags", "status", "last_backup_at"];
/**
 * Trailing header column of a Xirang export whose cells use the reversible
 * escape of `encodeExportCell`. Only the exact seven export fields followed by
 * this marker enable decoding; any other CSV keeps leading apostrophes as data.
 */
const ESCAPED_EXPORT_MARKER = "xirang_csv_escaped_v1";
const ESCAPE_PREFIX = "'";

/**
 * Spreadsheet-safe and reversible: a value that could start a formula, or that
 * already starts with the escape apostrophe, gains exactly one leading
 * apostrophe and is quoted. Decoding a marked export removes one apostrophe.
 */
function encodeExportCell(value: string): string {
  const safe = FORMULA_PREFIX.test(value) || value.startsWith(ESCAPE_PREFIX) ? `${ESCAPE_PREFIX}${value}` : value;
  if (safe !== value || /[",\r\n]/.test(safe)) {
    return `"${safe.replace(/"/g, '""')}"`;
  }
  return safe;
}

function isEscapedExportHeader(fields: readonly string[]): boolean {
  return fields.length === EXPORT_FIELDS.length + 1
    && fields[EXPORT_FIELDS.length] === ESCAPED_EXPORT_MARKER
    && EXPORT_FIELDS.every((name, index) => fields[index] === name);
}

type CSVRecord = { line: number; fields: string[] };

type TokenizeResult =
  | { ok: true; records: CSVRecord[] }
  | { ok: false; error: CSVNodeRowError };

/**
 * RFC 4180 tokenizer. A quote may only open a field and must be followed by a
 * delimiter, line break or end of input once closed; quoted fields keep commas,
 * escaped quotes and line breaks verbatim. CRLF, LF and lone CR end records.
 */
function tokenizeCSV(content: string): TokenizeResult {
  const text = content.startsWith("\uFEFF") ? content.slice(1) : content;
  const records: CSVRecord[] = [];
  let fields: string[] = [];
  let field = "";
  let state: "fieldStart" | "unquoted" | "quoted" | "afterQuote" = "fieldStart";
  let line = 1;
  let recordLine = 1;
  let quoteLine = 1;

  let i = 0;
  while (i < text.length) {
    const ch = text[i];
    if (state === "quoted") {
      if (ch === "\"") {
        if (text[i + 1] === "\"") {
          field += "\"";
          i += 2;
        } else {
          state = "afterQuote";
          i += 1;
        }
        continue;
      }
      if (ch === "\n" || (ch === "\r" && text[i + 1] !== "\n")) {
        line += 1;
      }
      field += ch;
      i += 1;
      continue;
    }
    if (ch === "\r" || ch === "\n") {
      fields.push(field);
      records.push({ line: recordLine, fields });
      fields = [];
      field = "";
      state = "fieldStart";
      i += ch === "\r" && text[i + 1] === "\n" ? 2 : 1;
      line += 1;
      recordLine = line;
      continue;
    }
    if (ch === ",") {
      fields.push(field);
      field = "";
      state = "fieldStart";
      i += 1;
      continue;
    }
    if (state === "afterQuote" || (ch === "\"" && state === "unquoted")) {
      return { ok: false, error: { line, code: "strayQuote" } };
    }
    if (ch === "\"") {
      state = "quoted";
      quoteLine = line;
    } else {
      field += ch;
      state = "unquoted";
    }
    i += 1;
  }

  if (state === "quoted") {
    return { ok: false, error: { line: quoteLine, code: "unclosedQuote" } };
  }
  if (fields.length > 0 || state !== "fieldStart") {
    fields.push(field);
    records.push({ line: recordLine, fields });
  }
  return { ok: true, records };
}

function textCell(record: CSVRecord, index: number, escaped: boolean): string {
  const raw = record.fields[index] ?? "";
  if (!escaped) return raw.trim();
  return raw.startsWith(ESCAPE_PREFIX) ? raw.slice(ESCAPE_PREFIX.length) : raw;
}

/**
 * Parses node CSV (`name,host,username,port,tags`, optional header, extra
 * trailing columns ignored). A structurally malformed file yields zero rows and
 * its single structural error; otherwise each invalid record contributes one
 * diagnostic in line order while valid records are still returned. Name, host,
 * username and tags of a marked Xirang export retain their original spacing and
 * spelling. Other files retain the usual import whitespace normalization.
 */
export function parseCSVNodeRows(content: string): CSVNodeParseResult {
  const tokenized = tokenizeCSV(content);
  if (!tokenized.ok) {
    return { rows: [], errors: [tokenized.error] };
  }

  const records = tokenized.records.filter((record) => record.fields.some((one) => one.trim() !== ""));
  const [first] = records;
  const escaped = first !== undefined && isEscapedExportHeader(first.fields);
  const hasHeader = escaped || (first?.fields[0]?.trim().toLowerCase() === "name"
    && first.fields[1]?.trim().toLowerCase() === "host");
  const body = hasHeader ? records.slice(1) : records;
  const rows: CSVNodeRow[] = [];
  const errors: CSVNodeRowError[] = [];

  for (const record of body) {
    const { line } = record;
    const name = textCell(record, 0, escaped);
    const host = textCell(record, 1, escaped);
    if (!name || !host) {
      errors.push({ line, code: "missingRequired" });
      continue;
    }

    // The username becomes the SSH login identity and has no server-side guard.
    // Check the raw cell so control-only input cannot collapse into the default.
    if (CONTROL_CHARACTER.test(record.fields[2] ?? "")) {
      errors.push({ line, code: "controlCharacter", field: "username" });
      continue;
    }
    const username = textCell(record, 2, escaped) || DEFAULT_USERNAME;

    const portRaw = (record.fields[3] ?? "").trim();
    let port = DEFAULT_PORT;
    if (portRaw !== "") {
      const parsed = DECIMAL_PORT.test(portRaw) ? Number(portRaw) : Number.NaN;
      if (!(parsed >= 1 && parsed <= 65535)) {
        errors.push({ line, code: "invalidPort", port: portRaw });
        continue;
      }
      port = parsed;
    }

    const tagCell = textCell(record, 4, escaped);
    const tags = escaped ? tagCell : tagCell.split(",").map((tag) => tag.trim()).filter(Boolean).join(",");
    rows.push({ name, host, username, port, tags });
  }

  return { rows, errors };
}

export function describeCSVNodeRowError(error: CSVNodeRowError, t: TFunction): string {
  switch (error.code) {
    case "unclosedQuote":
      return t("nodes.csvParseUnclosedQuote", { line: error.line });
    case "strayQuote":
      return t("nodes.csvParseStrayQuote", { line: error.line });
    case "missingRequired":
      return t("nodes.csvParseMissingRequired", { line: error.line });
    case "invalidPort":
      return t("nodes.csvParseInvalidPort", { line: error.line, port: error.port });
    case "controlCharacter":
      return t("nodes.csvParseControlCharacter", { line: error.line, field: error.field });
  }
}

export function buildNodesCSV(
  nodes: readonly Pick<NodeRecord, "name" | "host" | "username" | "port" | "tags" | "rawTags" | "status" | "lastBackupAt">[],
): string {
  const lines = [[...EXPORT_FIELDS, ESCAPED_EXPORT_MARKER].join(",")];
  for (const node of nodes) {
    lines.push([
      node.name,
      node.host,
      node.username,
      String(node.port),
      node.rawTags ?? node.tags.join(","),
      node.status,
      node.lastBackupAt,
    ].map(encodeExportCell).concat("").join(","));
  }
  return lines.join("\r\n");
}

export const NODES_CSV_TEMPLATE = [
  "name,host,username,port,tags",
  "prod-app-01,10.10.0.11,root,22,\"prod,app\"",
  "prod-db-01,10.10.0.21,root,22,\"prod,db\"",
].join("\r\n");

/** Legacy base path applied to CSV-imported nodes; disclose it before importing. */
export const CSV_IMPORT_BASE_PATH = "/";

/** `error` is null when createNode reported failure by returning a non-positive id. */
export type CSVNodeImportFailure = { name: string; error: unknown };

export type CSVNodeImportOutcome =
  | { status: "completed"; succeeded: number; failures: CSVNodeImportFailure[] }
  | { status: "cancelled" };

/**
 * Creates rows sequentially. The lifetime is checked before each write and
 * after each settles; once it ends no further write starts and no counts are
 * reported, so a stale session never surfaces a summary.
 */
export async function importCSVNodeRows(
  rows: readonly CSVNodeRow[],
  keyId: string,
  createNode: (input: NewNodeInput, isCurrent: () => boolean) => Promise<number>,
  isCurrent: () => boolean,
): Promise<CSVNodeImportOutcome> {
  let succeeded = 0;
  const failures: CSVNodeImportFailure[] = [];

  for (const row of rows) {
    if (!isCurrent()) {
      return { status: "cancelled" };
    }
    let nodeId: number;
    try {
      nodeId = await createNode({
        name: row.name,
        host: row.host,
        username: row.username,
        port: row.port,
        tags: row.tags,
        authType: "key",
        keyId,
        password: undefined,
        basePath: CSV_IMPORT_BASE_PATH,
      }, isCurrent);
    } catch (error) {
      if (!isCurrent()) {
        return { status: "cancelled" };
      }
      failures.push({ name: row.name, error });
      continue;
    }
    if (!isCurrent()) {
      return { status: "cancelled" };
    }
    if (nodeId > 0) {
      succeeded += 1;
    } else {
      failures.push({ name: row.name, error: null });
    }
  }

  if (!isCurrent()) {
    return { status: "cancelled" };
  }
  return { status: "completed", succeeded, failures };
}
