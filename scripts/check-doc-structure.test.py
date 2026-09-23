#!/usr/bin/env python3
"""以独立 Git fixture 验证文档检查，不依赖当前工作区的完整程度。"""
import importlib.util
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.dont_write_bytecode = True

# Fixture Git commands must never inherit the parent hook index/worktree.
for key in list(os.environ):
    if key.startswith("GIT_") or key == "GITHUB_BASE_REF":
        os.environ.pop(key)

spec = importlib.util.spec_from_file_location("doc_structure", Path(__file__).with_name("check-doc-structure.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class StructureTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        subprocess.run(["git", "init", "-q", str(self.root)], check=True)
        self.write("docs/README.md", "# 文档\n\n[合同](spec/README.md)\n")
        self.write("docs/spec/README.md", "# 开发合同\n\n[细则](rules.md#中文-规则)\n")
        self.write("docs/spec/rules.md", "# 规则\n\n## 中文 `规则`\n\n有效合同。\n")

    def tearDown(self):
        self.tmp.cleanup()

    def write(self, path, content):
        dest = self.root / path
        dest.parent.mkdir(parents=True, exist_ok=True)
        dest.write_text(content)

    def assert_problem(self, needle):
        self.assertTrue(any(needle in x for x in module.check(self.root)), module.check(self.root))

    def test_new_paths_chinese_anchor_and_untracked_docs(self):
        self.assertEqual(module.check(self.root), [])

    def test_missing_directory_entry(self):
        self.write("docs/new/topic.md", "# 主题\n")
        self.assert_problem("缺少 README.md")

    def test_empty_directory(self):
        (self.root / "docs/empty").mkdir()
        self.assert_problem("缺少 README.md")

    def test_docs_root_still_requires_readme_when_ignored(self):
        self.write(".gitignore", "docs/\n")
        (self.root / "docs/README.md").unlink()
        self.assert_problem("docs: 缺少 README.md")

    def test_ignored_directory_is_skipped(self):
        self.write(".gitignore", "docs/private/\n")
        self.write("docs/private/secret.md", "# 私有材料\n")
        self.assertEqual(module.check(self.root), [])

    def test_tracked_document_in_ignored_directory_still_requires_readme(self):
        self.write(".gitignore", "docs/private/\n")
        self.write("docs/private/contract.md", "# 合同\n")
        subprocess.run(
            ["git", "-C", str(self.root), "add", "-f", "--", "docs/private/contract.md"],
            check=True,
        )
        self.assert_problem("docs/private: 缺少 README.md")

        self.write(
            "docs/README.md",
            "# 文档\n\n[合同](spec/README.md)\n[私有合同](private/README.md)\n",
        )
        self.write("docs/private/README.md", "# 私有合同\n\n[合同正文](contract.md)\n")
        subprocess.run(
            ["git", "-C", str(self.root), "add", "-f", "--", "docs/private/README.md"],
            check=True,
        )
        self.assertEqual(module.check(self.root), [])

    def test_ignored_empty_directory_is_skipped(self):
        self.write(".gitignore", "docs/private/\n")
        (self.root / "docs/private/empty").mkdir(parents=True)
        self.assertEqual(module.check(self.root), [])

    def test_broken_relative_link(self):
        self.write("docs/spec/rules.md", "# 规则\n[坏链接](missing.md)\n")
        self.assert_problem("无效链接")

    def test_bad_anchor(self):
        self.write("docs/spec/rules.md", "# 规则\n## 不同标题\n")
        self.assert_problem("无效锚点")

    def test_unreachable_document(self):
        self.write("docs/spec/orphan.md", "# 无入口文档\n")
        self.assert_problem("无法从")

    def test_retired_path_in_hidden_config(self):
        self.write(".codex/agents/review.toml", 'contract = "spec/backend/index.md"\n')
        self.assert_problem("退役文档路径")

    def test_relative_retired_spec_consumer_and_current_spec(self):
        for target in ["./spec/backend/rules.md", "../spec/backend/rules.md", "foo/spec/backend/rules.md", "/checkout/spec/guides/rules.md"]:
            with self.subTest(target=target):
                self.write(".codex/config.toml", f'contract = "{target}"\n')
                self.assert_problem("退役文档路径")
        self.write(".codex/config.toml", 'contract = "../docs/spec/backend/rules.md"\n')
        self.assertEqual(module.check(self.root), [])

    def test_retired_build_exclusion(self):
        self.write(".dockerignore", "/spec\n")
        self.assert_problem("退役文档路径")

    def test_retired_markdown_target_even_when_file_exists(self):
        self.write("backend/README_backend.md", "# 旧后端入口\n")
        self.write("docs/spec/rules.md", "# 规则\n## 中文 `规则`\n[旧入口](../../backend/README_backend.md)\n")
        self.assert_problem("退役文档文件仍存在")
        self.assert_problem("退役链接")

    def test_retired_design_consumer_in_product_test(self):
        for target in ["DESIGN.md", "../DESIGN.md", "../../DESIGN.md", "/checkout/DESIGN.md"]:
            with self.subTest(target=target):
                self.write("web/src/theme.test.ts", f'readFileSync("{target}");\n')
                self.assert_problem("退役文档路径")

    def test_old_root_and_old_markdown_link(self):
        self.write("spec/README.md", "# 旧目录\n")
        self.write("docs/spec/rules.md", "# 规则\n[旧](../../spec/backend/index.md)\n")
        self.assert_problem("退役根目录")
        self.assert_problem("退役链接")

    def test_reference_link_and_duplicate_heading(self):
        self.write("docs/spec/rules.md", "# 规则\n## 中文 `规则`\n## 中文 `规则`\n[重复][x]\n\n[x]: #中文-规则-1\n")
        self.assertEqual(module.check(self.root), [])

    def test_fenced_example_is_not_link_or_heading(self):
        self.write("docs/spec/rules.md", "# 规则\n## 中文 `规则`\n```md\n[示例](absent.md)\n## 示例\n```\n")
        self.assertEqual(module.check(self.root), [])

    def test_placeholder(self):
        self.write("docs/spec/rules.md", "# 规则\n待补充\n")
        self.assert_problem("占位正文")


if __name__ == "__main__":
    unittest.main()
