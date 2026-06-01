"""Tests for deterministic reconnaissance cookbook generation."""

import json
import textwrap

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
