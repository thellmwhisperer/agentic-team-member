"""Tests for deterministic reconnaissance cookbook generation."""

import json
import textwrap

import agentic_tdd_runner.recon as recon
from agentic_tdd_runner.recon import build_recon_cookbook


def _write_file(tmp_path, relative_path, content):
    path = tmp_path / relative_path
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(textwrap.dedent(content))
    return path


def test_recon_detects_next_stack_consumers_and_anti_anchor(tmp_path):
    _write_file(
        tmp_path,
        "package.json",
        json.dumps({
            "dependencies": {
                "next": "^16.0.0",
                "react": "^19.0.0",
                "@testing-library/react": "^16.0.0",
            },
            "devDependencies": {"jest": "^30.0.0"},
            "scripts": {"test": "jest"},
        }),
    )
    _write_file(tmp_path, "next.config.js", "module.exports = {};\n")
    _write_file(
        tmp_path,
        "app/products/page.tsx",
        """
        import { ProductCard } from '../../src/common/ProductCard';

        export default function ProductsPage() {
          return <ProductCard />;
        }
        """,
    )
    _write_file(
        tmp_path,
        "src/common/ProductCard.tsx",
        """
        export function ProductCard() {
          return <p>Loading...</p>;
        }
        """,
    )
    _write_file(
        tmp_path,
        "app/products/page.test.tsx",
        """
        import { render } from '@testing-library/react';
        import ProductsPage from './page';

        test('renders products', () => {
          render(<ProductsPage />);
        });
        """,
    )

    cookbook = build_recon_cookbook(
        issue_text=(
            "Bug: `src/common/ProductCard.tsx` flashes stale data during hydration. "
            "`ProductCard` renders correctly after refresh."
        ),
        project_root=str(tmp_path),
    )

    assert cookbook.markdown.startswith("## Recon Cookbook")
    assert "Next.js" in cookbook.markdown
    assert "App Router" in cookbook.markdown
    assert "jest" in cookbook.markdown
    assert "@testing-library/react" in cookbook.markdown
    assert "Anti-anchor" in cookbook.markdown
    assert "src/common/ProductCard.tsx" in cookbook.markdown
    assert "app/products/page.tsx" in cookbook.markdown
    assert "app/products/page.test.tsx" in cookbook.markdown
    assert "Locate -> trace -> distinguish symptom from cause -> test + fix." in cookbook.markdown

    payload = cookbook.to_log_dict()
    assert payload["frameworks"] == ["Next.js"]
    assert payload["candidate_consumers"][0]["path"] == "app/products/page.tsx"
    assert payload["anti_anchor_paths"] == ["src/common/ProductCard.tsx"]


def test_recon_stays_empty_when_no_mechanical_facts_exist(tmp_path):
    cookbook = build_recon_cookbook(
        issue_text="Bug: totals are wrong",
        project_root=str(tmp_path),
    )

    assert cookbook.markdown == ""
    assert cookbook.to_log_dict()["sections"] == []


def test_recon_bare_symbol_extraction_ignores_prose_capitalized_words(tmp_path):
    _write_file(
        tmp_path,
        "src/page.tsx",
        """
        import { ProductCard } from './common/ProductCard';

        export function Page() {
          return <ProductCard />;
        }
        """,
    )
    _write_file(
        tmp_path,
        "src/api.ts",
        """
        export const API = {};
        export const Response = {};
        """,
    )

    cookbook = build_recon_cookbook(
        issue_text="The API Response says ProductCard flashes stale data.",
        project_root=str(tmp_path),
    )

    assert [consumer.to_log_dict() for consumer in cookbook.candidate_consumers] == [
        {"symbol": "ProductCard", "path": "src/page.tsx"},
    ]


def test_recon_does_not_treat_pyproject_as_python_framework_in_mixed_repo(tmp_path):
    _write_file(
        tmp_path,
        "package.json",
        json.dumps({"dependencies": {"next": "^16.0.0"}}),
    )
    _write_file(tmp_path, "pyproject.toml", "[tool.ruff]\n")

    cookbook = build_recon_cookbook(
        issue_text="Bug: page flashes stale data",
        project_root=str(tmp_path),
    )

    assert cookbook.frameworks == ["Next.js"]
    assert "Python" not in cookbook.markdown


def test_recon_skips_non_utf8_source_files(tmp_path):
    bad = tmp_path / "src" / "bad.ts"
    bad.parent.mkdir(parents=True, exist_ok=True)
    bad.write_bytes(b"\xff\xfe\xfa")

    cookbook = build_recon_cookbook(
        issue_text="Bug: BadThing flashes",
        project_root=str(tmp_path),
    )

    assert cookbook.candidate_consumers == []


def test_recon_walks_source_files_once_per_build(tmp_path, monkeypatch):
    _write_file(
        tmp_path,
        "package.json",
        json.dumps({
            "dependencies": {"next": "^16.0.0"},
            "devDependencies": {"jest": "^30.0.0"},
        }),
    )
    consumer = _write_file(
        tmp_path,
        "app/page.tsx",
        """
        "use client";
        import { ProductCard } from '../src/common/ProductCard';

        export function Page() {
          return <ProductCard />;
        }
        """,
    )
    test = _write_file(tmp_path, "app/page.test.tsx", "test('page', () => {});\n")
    calls = []

    def fake_source_files(root):
        calls.append(root)
        return [tmp_path / consumer, tmp_path / test]

    monkeypatch.setattr(recon, "_source_files", fake_source_files)

    cookbook = build_recon_cookbook(
        issue_text="Bug: ProductCard flashes stale data.",
        project_root=str(tmp_path),
    )

    assert len(calls) == 1
    assert cookbook.candidate_consumers
