#!/usr/bin/env python3
"""检查维护文档入口、相对链接、锚点、可达性与退役引用（含点目录）。"""

import argparse
import re
import subprocess
import unicodedata
from pathlib import Path
from urllib.parse import unquote, urlsplit


def prose(text):
    lines = []
    fence = None
    for line in text.splitlines():
        match = re.match(r"^\s*(`{3,}|~{3,})", line)
        if match:
            marker = match[1]
            if fence is None:
                fence = marker
            elif marker[0] == fence[0] and len(marker) >= len(fence):
                fence = None
            lines.append("")
        else:
            lines.append(line if fence is None else "")
    return "\n".join(lines)


def anchors(text):
    result = set(re.findall(r'<(?:a|h[1-6])\b[^>]*(?:id|name)=["\']([^"\']+)', text))
    counts = {}
    for line in prose(text).splitlines():
        heading = re.match(r"^ {0,3}#{1,6}\s+(.+?)(?:\s+#+)?$", line)
        if not heading:
            continue
        title = re.sub(r"!?\[([^]]*)\]\([^)]*\)", r"\1", heading[1])
        title = re.sub(r"<[^>]+>", "", title).replace("`", "").lower()
        slug = "".join(c for c in title if c in "-_ " or unicodedata.category(c)[0] in "LN").replace(" ", "-")
        count = counts.get(slug, 0)
        counts[slug] = count + 1
        result.add(slug if count == 0 else f"{slug}-{count}")
    return result


def links(text):
    text = re.sub(r"(`+).*?\1", "", prose(text))
    definitions = {}
    for label, target in re.findall(r'^ {0,3}\[([^]]+)\]:\s*<?([^\s>]+)>?', text, re.M):
        definitions[label.casefold()] = target
    for match in re.finditer(r"!?\[[^]\n]*\]\(\s*(<[^>]+>|[^\s)]+)(?:\s+[^)]*)?\)", text):
        yield match[1].strip("<>")
    for match in re.finditer(r"!?\[([^]\n]+)\]\[([^]\n]*)\]", text):
        label = (match[2] or match[1]).casefold()
        yield definitions.get(label, "__missing_reference__/" + label)
    # 引用定义本身也必须指向有效资源。
    yield from definitions.values()


def check(root):
    root = root.resolve()
    paths = subprocess.check_output(
        ["git", "-C", str(root), "ls-files", "-co", "--exclude-standard", "-z"]
    ).decode().split("\0")
    files = sorted({root / p for p in paths if p and (root / p).is_file()})
    errors = []
    docs_root = root / "docs"
    visible_doc_directories = set()
    for path in files:
        if path.is_relative_to(docs_root):
            directory = path.parent
            while directory.is_relative_to(docs_root):
                visible_doc_directories.add(directory)
                if directory == docs_root:
                    break
                directory = directory.parent
    directories = [docs_root, *[
        directory for directory in docs_root.rglob("*") if directory.is_dir()
    ]]
    ignored_directories = set()
    if directories:
        directory_paths = [
            (directory.relative_to(root).as_posix() + "/")
            for directory in directories
        ]
        result = subprocess.run(
            ["git", "-C", str(root), "check-ignore", "--stdin", "-z"],
            input="\0".join(directory_paths).encode(),
            capture_output=True,
        )
        if result.returncode in (0, 1):
            ignored_directories = set(result.stdout.decode().split("\0")) - {""}
        else:
            detail = result.stderr.decode(errors="replace").strip()
            errors.append(
                f"docs/: git check-ignore 失败"
                f"{': ' + detail if detail else f'（退出码 {result.returncode}）'}"
            )
    documents = [p for p in files if p.suffix == ".md" and p.name != "CHANGELOG.md"]
    graph = {p: set() for p in documents}
    retired = re.compile(
        r"(?<!docs/)(?<![\w-])spec/(?:backend|frontend|guides|index\.md)"
        r"|^/?spec/?$|README_backend\.md|backup-assets-(?:load|slo)\.md"
        r"|backup-file-catalog\.md|backup-content-transport\.md"
        r"|docs/spec/(?:backend/|frontend/|guides/)?index\.md"
        r"|(?<![\w.-])DESIGN\.md"
    )
    if (root / "spec").exists():
        errors.append("spec/: 退役根目录仍存在")
    for directory in directories:
        rel = directory.relative_to(root).as_posix()
        if (
            directory != docs_root
            and rel + "/" in ignored_directories
            and directory not in visible_doc_directories
        ):
            continue
        if not (directory / "README.md").is_file():
            errors.append(f"{rel}: 缺少 README.md 入口")
    for path in files:
        rel = path.relative_to(root).as_posix()
        if retired.search(rel):
            errors.append(f"{rel}: 退役文档文件仍存在")
        # 历史生成记录和故意引用旧路径的负例不属于当前消费者。
        if path.name == "CHANGELOG.md" or rel in {
            "scripts/check-doc-freshness.test.sh", "scripts/check-migration-version.test.sh",
            "scripts/check-doc-structure.test.py", "scripts/check-doc-structure.py",
        }:
            continue
        try:
            text = path.read_text(encoding="utf-8")
        except (UnicodeError, OSError):
            continue
        if "\0" in text:
            continue
        for number, line in enumerate(text.splitlines(), 1):
            # Markdown 链接按下方真实相对目标解析，允许 docs 内的 spec/ 相对链接。
            scan_line = re.sub(r"\]\([^)]*\)", "]", line) if path.suffix == ".md" else line
            if retired.search(scan_line):
                errors.append(f"{rel}:{number}: 退役文档路径")
    for path in documents:
        text = path.read_text(encoding="utf-8")
        rel = path.relative_to(root)
        if re.search(r"^(?:\s*\*\*)?(?:To be filled|待补充|占位文档)(?:\*\*)?\s*[。.!]?\s*$", text, re.M | re.I):
            errors.append(f"{rel}: 占位正文")
        for target in links(text):
            parsed = urlsplit(target)
            if parsed.scheme or parsed.netloc:
                continue
            target_path = unquote(parsed.path)
            dest = (root / target_path.lstrip("/") if target_path.startswith("/") else path.parent / target_path).resolve() if target_path else path
            if not dest.is_relative_to(root):
                errors.append(f"{rel}: 链接越出仓库 {target}")
                continue
            if retired.search(dest.relative_to(root).as_posix()):
                errors.append(f"{rel}: 退役链接 {target}")
                continue
            if not dest.exists():
                errors.append(f"{rel}: 无效链接 {target}")
                continue
            if dest.is_dir() and (dest / "README.md").is_file():
                dest /= "README.md"
            if dest in graph:
                graph[path].add(dest)
            if parsed.fragment and dest.suffix == ".md" and unquote(parsed.fragment) not in anchors(dest.read_text(encoding="utf-8")):
                errors.append(f"{rel}: 无效锚点 {target}")
    seen = set()
    queue = [root / "docs/README.md"]
    while queue:
        node = queue.pop()
        if node not in seen:
            seen.add(node)
            queue.extend(graph.get(node, ()))
    for path in documents:
        if path.is_relative_to(root / "docs") and path not in seen:
            errors.append(f"{path.relative_to(root)}: 无法从 docs/README.md 到达")
    return errors


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parent.parent)
    args = parser.parse_args()
    problems = check(args.root)
    for problem in problems:
        print(problem)
    print(f"文档结构检查：{'FAIL' if problems else 'PASS'}（{len(problems)} 项）")
    raise SystemExit(bool(problems))
