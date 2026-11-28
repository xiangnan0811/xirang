import type { TFunction } from "i18next";
import { describe, expect, it, vi } from "vitest";
import {
  buildNodesCSV,
  describeCSVNodeRowError,
  importCSVNodeRows,
  NODES_CSV_TEMPLATE,
  parseCSVNodeRows,
  type CSVNodeRow,
  type CSVNodeRowError,
} from "@/pages/nodes-page.csv";

const EXPORT_HEADER = "name,host,username,port,tags,status,last_backup_at,xirang_csv_escaped_v1";

function exportNamed(name: string): string {
  return buildNodesCSV([{
    name,
    host: "10.0.0.1",
    username: "root",
    port: 22,
    tags: [],
    status: "online",
    lastBackupAt: "2026-01-01 00:00:00",
  }]);
}

function exportedRow(nameCell: string): string {
  return `${EXPORT_HEADER}\r\n${nameCell},10.0.0.1,root,22,,online,2026-01-01 00:00:00,`;
}

describe("buildNodesCSV 单元格编码", () => {
  it.each([
    ["hello", "hello"],
    ["a,b", '"a,b"'],
    ['say "hi"', '"say ""hi"""'],
    ["line1\nline2", '"line1\nline2"'],
    ["line1\rline2", '"line1\rline2"'],
    [" hello", " hello"],
    ["it's", "it's"],
  ])("普通值 %j 仅按 RFC 4180 转义", (input, expected) => {
    expect(exportNamed(input)).toBe(exportedRow(expected));
  });

  it.each([
    ["=SUM(A1)", "\"'=SUM(A1)\""],
    ["+cmd", "\"'+cmd\""],
    ["-cmd", "\"'-cmd\""],
    ["@import", "\"'@import\""],
    ["\tcmd", "\"'\tcmd\""],
    ["\rcmd", "\"'\rcmd\""],
    [" =HYPERLINK()", "\"' =HYPERLINK()\""],
    ["  +cmd", "\"'  +cmd\""],
    [" \t-payload", "\"' \t-payload\""],
    ["   @import", "\"'   @import\""],
  ])("公式前缀（含前导空白）%j → 前置单引号并包裹", (input, expected) => {
    expect(exportNamed(input)).toBe(exportedRow(expected));
  });

  it.each([
    ["'quoted", "\"''quoted\""],
    ["''", "\"'''\""],
    ["'=x", "\"''=x\""],
  ])("已以单引号开头的值 %j 同样前置单引号，保证可逆", (input, expected) => {
    expect(exportNamed(input)).toBe(exportedRow(expected));
  });
});

describe("buildNodesCSV", () => {
  const nodes = [
    {
      name: 'node "edge", A',
      host: "10.0.0.5",
      username: "ubuntu",
      port: 2222,
      tags: ["prod", "edge"],
      status: "online" as const,
      lastBackupAt: "-",
    },
    {
      name: '=HYPERLINK("x")',
      host: "10.0.0.6",
      username: "-root",
      port: 22,
      tags: ["@ops"],
      status: "offline" as const,
      lastBackupAt: "2026-01-01 00:00:00",
    },
  ];

  it("表头追加 xirang_csv_escaped_v1 标记，行补齐为同列数并对所有文本单元格做公式防护", () => {
    expect(buildNodesCSV(nodes)).toBe([
      EXPORT_HEADER,
      "\"node \"\"edge\"\", A\",10.0.0.5,ubuntu,2222,\"prod,edge\",online,\"'-\",",
      "\"'=HYPERLINK(\"\"x\"\")\",10.0.0.6,\"'-root\",22,\"'@ops\",offline,2026-01-01 00:00:00,",
    ].join("\r\n"));
  });

  it("导出再导入无损还原公式前缀与字面单引号", () => {
    const tricky = [
      ...nodes,
      {
        name: "'quoted",
        host: "-edge.example",
        username: "'admin",
        port: 2200,
        tags: ["'a", "=b"],
        status: "online" as const,
        lastBackupAt: "-",
      },
      {
        name: "''=x",
        host: "10.0.0.8",
        username: "+ops",
        port: 22,
        tags: ["it's"],
        status: "online" as const,
        lastBackupAt: "-",
      },
      {
        name: " =edge ",
        host: "edge.example",
        username: " ops ",
        port: 22,
        tags: ["prod", "audit"],
        rawTags: " prod,, audit ",
        status: "online" as const,
        lastBackupAt: "-",
      },
    ];
    expect(parseCSVNodeRows(buildNodesCSV(tricky))).toEqual({
      rows: [
        { name: 'node "edge", A', host: "10.0.0.5", username: "ubuntu", port: 2222, tags: "prod,edge" },
        { name: '=HYPERLINK("x")', host: "10.0.0.6", username: "-root", port: 22, tags: "@ops" },
        { name: "'quoted", host: "-edge.example", username: "'admin", port: 2200, tags: "'a,=b" },
        { name: "''=x", host: "10.0.0.8", username: "+ops", port: 22, tags: "it's" },
        { name: " =edge ", host: "edge.example", username: " ops ", port: 22, tags: " prod,, audit " },
      ],
      errors: [],
    });
  });

  it("仅表头时也输出带标记的表头", () => {
    expect(buildNodesCSV([])).toBe(EXPORT_HEADER);
    expect(parseCSVNodeRows(buildNodesCSV([]))).toEqual({ rows: [], errors: [] });
  });
});

describe("parseCSVNodeRows 单引号解码", () => {
  const row = "\"'=x\",\"'-h\",\"''u\",\"'22\",\"'a,'b\",online,-";

  it("仅标记导出的名称/主机/用户名/标签去掉一个前导单引号，端口与标签内部不解码", () => {
    expect(parseCSVNodeRows(`${EXPORT_HEADER}\r\n${row},`)).toEqual({
      rows: [],
      errors: [{ line: 2, code: "invalidPort", port: "'22" }],
    });
    expect(parseCSVNodeRows(`${EXPORT_HEADER}\r\n"'=x","'-h","''u",22,"'a,'b",online,-,`).rows).toEqual([
      { name: "=x", host: "-h", username: "'u", port: 22, tags: "a,'b" },
    ]);
  });

  it.each([
    ["外部五列表头", "name,host,username,port,tags"],
    ["旧版导出表头（无标记）", "name,host,username,port,tags,status,last_backup_at"],
    ["标记不在第八列", "name,host,username,port,tags,xirang_csv_escaped_v1"],
    ["表头大小写不同", "Name,host,username,port,tags,status,last_backup_at,xirang_csv_escaped_v1"],
    ["表头含空白", " name,host,username,port,tags,status,last_backup_at,xirang_csv_escaped_v1"],
    ["标记大小写不同", "name,host,username,port,tags,status,last_backup_at,XIRANG_CSV_ESCAPED_V1"],
    ["标记后还有其他列", `${EXPORT_HEADER},notes`],
  ])("%s：前导单引号按字面保留", (_label, header) => {
    expect(parseCSVNodeRows(`${header}\r\n"'=x","'h","'u",22,"'a,'b"`).rows).toEqual([
      { name: "'=x", host: "'h", username: "'u", port: 22, tags: "'a,'b" },
    ]);
  });

  it("无表头文件中的前导单引号按字面保留", () => {
    expect(parseCSVNodeRows("'n,'h,'u,22,'t").rows).toEqual([
      { name: "'n", host: "'h", username: "'u", port: 22, tags: "'t" },
    ]);
  });
});

describe("parseCSVNodeRows", () => {
  it.each([
    ["引号内仅 CRLF", "\"\r\n\""],
    ["引号内仅制表符", "\"\t\""],
    ["空格与制表符混合", " \t "],
    ["空格包裹的换行", "\" \n \""],
    ["有效名前导制表符", "\troot"],
    ["DEL", "ro\x7fot"],
    ["C1 换行 NEL", "\x85"],
    ["Unicode 行分隔符", "\u2028"],
  ])("用户名%s → 控制字符错误，不回退为 root", (_label, cell) => {
    expect(parseCSVNodeRows(`n,10.0.0.1,${cell},22,`)).toEqual({
      rows: [],
      errors: [{ line: 1, code: "controlCharacter", field: "username" }],
    });
  });

  it("标记导出中带转义前缀的控制字符用户名同样拒绝", () => {
    expect(parseCSVNodeRows(`${EXPORT_HEADER}\r\nn,10.0.0.1,"'\t",22,,online,-,`)).toEqual({
      rows: [],
      errors: [{ line: 2, code: "controlCharacter", field: "username" }],
    });
  });

  it("仅空格的用户名视为空单元格并使用默认 root", () => {
    expect(parseCSVNodeRows("n,10.0.0.1,   ,22,").rows).toEqual([
      { name: "n", host: "10.0.0.1", username: "root", port: 22, tags: "" },
    ]);
  });

  it("去除 BOM、识别 CRLF 与带引号的逗号标签，空用户名/端口使用默认值", () => {
    const csv = "\uFEFFname,host,username,port,tags\r\nprod-app-01,10.10.0.11,,,\"prod, app ,\"\r\n";
    expect(parseCSVNodeRows(csv)).toEqual({
      rows: [{ name: "prod-app-01", host: "10.10.0.11", username: "root", port: 22, tags: "prod,app" }],
      errors: [],
    });
  });

  it("模板可直接导入，竖线作为标签字面字符保留", () => {
    expect(parseCSVNodeRows(NODES_CSV_TEMPLATE)).toEqual({
      rows: [
        { name: "prod-app-01", host: "10.10.0.11", username: "root", port: 22, tags: "prod,app" },
        { name: "prod-db-01", host: "10.10.0.21", username: "root", port: 22, tags: "prod,db" },
      ],
      errors: [],
    });
    expect(parseCSVNodeRows("n,10.0.0.1,root,22,prod|edge").rows).toEqual([
      { name: "n", host: "10.0.0.1", username: "root", port: 22, tags: "prod|edge" },
    ]);
  });

  it("无表头、单独 CR 换行与多余尾列均可解析", () => {
    expect(parseCSVNodeRows("a,10.0.0.1\rb,10.0.0.2,admin,2200,x,online,-")).toEqual({
      rows: [
        { name: "a", host: "10.0.0.1", username: "root", port: 22, tags: "" },
        { name: "b", host: "10.0.0.2", username: "admin", port: 2200, tags: "x" },
      ],
      errors: [],
    });
  });

  it("空内容与仅表头返回空结果且无错误", () => {
    expect(parseCSVNodeRows("")).toEqual({ rows: [], errors: [] });
    expect(parseCSVNodeRows("\uFEFFname,host,username,port,tags\r\n")).toEqual({ rows: [], errors: [] });
  });

  it("引号内多行字段按结构识别并保留，后续行号据实推进", () => {
    const csv = [
      "name,host,username,port,tags",
      'n-1,10.0.0.1,root,22,"a',
      'b"',
      ",10.0.0.2",
    ].join("\r\n");
    expect(parseCSVNodeRows(csv)).toEqual({
      rows: [{ name: "n-1", host: "10.0.0.1", username: "root", port: 22, tags: "a\r\nb" }],
      errors: [{ line: 4, code: "missingRequired" }],
    });
  });

  it("未闭合引号属于结构错误：即使前面有有效行也返回零行", () => {
    const csv = [
      "name,host,username,port,tags",
      "n-ok,10.0.0.1,root,22,prod",
      'n-bad,10.0.0.2,root,22,"unclosed',
      "n-next,10.0.0.3,root,22,dr",
    ].join("\n");
    expect(parseCSVNodeRows(csv)).toEqual({ rows: [], errors: [{ line: 3, code: "unclosedQuote" }] });
  });

  it.each([
    ["非引号字段中出现引号", 'n-ok,10.0.0.1\nn"bad,10.0.0.2', 2],
    ["闭合引号后紧跟字符", '"a"b,10.0.0.1', 1],
    ["闭合引号后紧跟空格", '"a" ,10.0.0.1', 1],
    ["多行字段之后的行号", 'x,"multi\r\nline",z\r\nbad,"q"x', 3],
  ])("%s → strayQuote 结构错误并返回零行", (_label, csv, line) => {
    expect(parseCSVNodeRows(csv)).toEqual({ rows: [], errors: [{ line, code: "strayQuote" }] });
  });

  it("独立的无效行按行号顺序报告，且与有效行共存", () => {
    const csv = [
      "name,host,username,port,tags",
      "n-1,10.0.0.1,root,1,a",
      ",10.0.0.2,root,22,a",
      "n-3,10.0.0.3,root,65536,a",
      "n-4,10.0.0.4,root,65535,a",
      "n-5,10.0.0.5,root,22.5,a",
      "n-6,10.0.0.6,root,+22,a",
      "n-7,10.0.0.7,root,1e3,",
      "n-8,10.0.0.8,root,0x16,",
      "n-9,10.0.0.9,root,0,",
      "n-10,10.0.0.10,root, 022 ,ops",
      "n-11,  ,root,22,",
      "",
      ",,,,",
      'n-12,10.0.0.12,"ro',
      'ot",22,',
      "n-13,10.0.0.13",
    ].join("\n");
    expect(parseCSVNodeRows(csv)).toEqual({
      rows: [
        { name: "n-1", host: "10.0.0.1", username: "root", port: 1, tags: "a" },
        { name: "n-4", host: "10.0.0.4", username: "root", port: 65535, tags: "a" },
        { name: "n-10", host: "10.0.0.10", username: "root", port: 22, tags: "ops" },
        { name: "n-13", host: "10.0.0.13", username: "root", port: 22, tags: "" },
      ],
      errors: [
        { line: 3, code: "missingRequired" },
        { line: 4, code: "invalidPort", port: "65536" },
        { line: 6, code: "invalidPort", port: "22.5" },
        { line: 7, code: "invalidPort", port: "+22" },
        { line: 8, code: "invalidPort", port: "1e3" },
        { line: 9, code: "invalidPort", port: "0x16" },
        { line: 10, code: "invalidPort", port: "0" },
        { line: 12, code: "missingRequired" },
        { line: 15, code: "controlCharacter", field: "username" },
      ],
    });
  });
});

describe("describeCSVNodeRowError", () => {
  it("将诊断映射为带行号的本地化文案键", () => {
    const t = vi.fn((key: string, options: Record<string, unknown>) => `${key}:${JSON.stringify(options)}`);
    const render = (error: CSVNodeRowError) => describeCSVNodeRowError(error, t as unknown as TFunction);

    expect(render({ line: 3, code: "unclosedQuote" })).toBe('nodes.csvParseUnclosedQuote:{"line":3}');
    expect(render({ line: 2, code: "strayQuote" })).toBe('nodes.csvParseStrayQuote:{"line":2}');
    expect(render({ line: 4, code: "missingRequired" })).toBe('nodes.csvParseMissingRequired:{"line":4}');
    expect(render({ line: 5, code: "invalidPort", port: "0" }))
      .toBe('nodes.csvParseInvalidPort:{"line":5,"port":"0"}');
    expect(render({ line: 6, code: "controlCharacter", field: "username" }))
      .toBe('nodes.csvParseControlCharacter:{"line":6,"field":"username"}');
  });
});

describe("importCSVNodeRows", () => {
  const rows: CSVNodeRow[] = ["a", "b", "c", "d"].map((name, index) => ({
    name,
    host: `10.0.0.${index + 1}`,
    username: "root",
    port: 22,
    tags: "prod",
  }));

  it("以密钥认证与显式旧版基础路径 / 逐行创建，-1 与异常均计为失败", async () => {
    const failure = new Error("duplicate");
    const createNode = vi.fn()
      .mockResolvedValueOnce(11)
      .mockResolvedValueOnce(-1)
      .mockRejectedValueOnce(failure)
      .mockResolvedValueOnce(12);
    const isCurrent = () => true;

    await expect(importCSVNodeRows(rows, "key-1", createNode, isCurrent)).resolves.toEqual({
      status: "completed",
      succeeded: 2,
      failures: [{ name: "b", error: null }, { name: "c", error: failure }],
    });
    expect(createNode).toHaveBeenCalledTimes(4);
    expect(createNode.mock.calls[0]).toEqual([
      {
        name: "a",
        host: "10.0.0.1",
        username: "root",
        port: 22,
        tags: "prod",
        authType: "key",
        keyId: "key-1",
        password: undefined,
        basePath: "/",
      },
      isCurrent,
    ]);
    expect(createNode.mock.calls[0][1]).toBe(isCurrent);
  });

  it("生命周期在首次写入前已结束时不发起写入", async () => {
    const createNode = vi.fn();
    await expect(importCSVNodeRows(rows, "key-1", createNode, () => false)).resolves.toEqual({ status: "cancelled" });
    await expect(importCSVNodeRows([], "key-1", createNode, () => false)).resolves.toEqual({ status: "cancelled" });
    expect(createNode).not.toHaveBeenCalled();
  });

  it("写入完成时生命周期已结束则取消，不计数也不发起后续写入", async () => {
    let current = true;
    const createNode = vi.fn(async () => {
      current = false;
      return 21;
    });
    await expect(importCSVNodeRows(rows, "key-1", createNode, () => current)).resolves.toEqual({ status: "cancelled" });
    expect(createNode).toHaveBeenCalledTimes(1);
  });

  it("写入失败时生命周期已结束则取消，不报告失败也不发起后续写入", async () => {
    let current = true;
    const createNode = vi.fn(async () => {
      current = false;
      throw new Error("stale");
    });
    await expect(importCSVNodeRows(rows, "key-1", createNode, () => current)).resolves.toEqual({ status: "cancelled" });
    expect(createNode).toHaveBeenCalledTimes(1);
  });

  it("无行且生命周期有效时返回零计数", async () => {
    await expect(importCSVNodeRows([], "key-1", vi.fn(), () => true)).resolves.toEqual({
      status: "completed",
      succeeded: 0,
      failures: [],
    });
  });
});
