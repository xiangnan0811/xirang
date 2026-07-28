import { readFileSync } from "node:fs";
import path from "node:path";
import { AtRule, parse, Rule } from "postcss";
import type { ChildNode, Node, Root } from "postcss";
import { describe, expect, it } from "vitest";

const css = readFileSync(path.join(process.cwd(), "src/index.css"), "utf8");
const root = parse(css);

function mediaAncestors(node: ChildNode): string[] {
  const media: string[] = [];
  let current: Node | undefined = node.parent;

  while (current) {
    if (current instanceof AtRule && current.name === "media") {
      media.push(current.params.replace(/\s+/g, " ").trim());
    }
    current = current.parent;
  }

  return media;
}

function hasSelectors(rule: Rule, selectors: readonly string[]): boolean {
  return selectors.every((selector) => rule.selectors.includes(selector));
}

function normalizePowerSaveDescendantSelector(selector: string): string | null {
  const directPowerSaveDescendant = /^html\[\s*data-power\s*=\s*(?:"save"|'save'|save)\s*\]\s+\*$/i;

  return directPowerSaveDescendant.test(selector.trim())
    ? 'html[data-power="save"] *'
    : null;
}

function durationMilliseconds(value: string): number | null {
  const duration = value.trim().match(/^([+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:e[+-]?\d+)?)(ms|s)$/i);
  if (!duration) return null;

  const amount = Number(duration[1]);
  if (!Number.isFinite(amount)) return null;
  return duration[2].toLowerCase() === "s" ? amount * 1_000 : amount;
}

function powerSaveTransitionShortenings(stylesheet: Root): Array<{
  important: boolean;
  media: string[];
  property: string;
  selector: string;
  value: string;
}> {
  const transitions: Array<{
    important: boolean;
    media: string[];
    property: string;
    selector: string;
    value: string;
  }> = [];

  stylesheet.walkDecls(/^(?:transition|transition-duration)$/i, (declaration) => {
    const powerSaveSelectors: string[] = [];
    let current: Node | undefined = declaration.parent;

    while (current) {
      if (current instanceof Rule) {
        powerSaveSelectors.push(...current.selectors
          .map(normalizePowerSaveDescendantSelector)
          .filter((selector): selector is string => selector !== null));
      }
      current = current.parent;
    }

    if (powerSaveSelectors.length === 0) return;

    for (const selector of powerSaveSelectors) {
      transitions.push({
        important: declaration.important,
        media: mediaAncestors(declaration),
        property: declaration.prop.toLowerCase(),
        selector,
        value: declaration.value.trim(),
      });
    }
  });

  return transitions;
}

function reducedMotionTransitionDurations(stylesheet: Root): Array<{
  durationMs: number | null;
  important: boolean;
}> {
  const transitions: Array<{ durationMs: number | null; important: boolean }> = [];

  stylesheet.walkRules((rule) => {
    if (!hasSelectors(rule, ["*", "*::before", "*::after"])) return;
    if (!mediaAncestors(rule).includes("(prefers-reduced-motion: reduce)")) return;
    rule.walkDecls("transition-duration", (declaration) => {
      transitions.push({
        durationMs: durationMilliseconds(declaration.value),
        important: declaration.important,
      });
    });
  });

  return transitions;
}

describe("power-save transition declaration collection", () => {
  it("collects unguarded transition-duration shortenings regardless of duration", () => {
    const fixture = parse(`
      html[data-power="save"] * {
        transition-duration: 40ms !important;
      }
    `);

    expect(powerSaveTransitionShortenings(fixture)).toEqual([{
      important: true,
      media: [],
      property: "transition-duration",
      selector: 'html[data-power="save"] *',
      value: "40ms",
    }]);
  });

  it("collects unguarded transition shorthand declarations", () => {
    const fixture = parse(`
      html[data-power="save"] * {
        transition: opacity 40ms ease-in !important;
      }
    `);

    expect(powerSaveTransitionShortenings(fixture)).toEqual([{
      important: true,
      media: [],
      property: "transition",
      selector: 'html[data-power="save"] *',
      value: "opacity 40ms ease-in",
    }]);
  });

  it("collects media ancestry from nested transition declarations", () => {
    const fixture = parse(`
      html[data-power="save"] * {
        @media (prefers-reduced-motion: no-preference) {
          transition-duration: 40ms !important;
        }
      }
    `);

    expect(powerSaveTransitionShortenings(fixture)).toEqual([{
      important: true,
      media: ["(prefers-reduced-motion: no-preference)"],
      property: "transition-duration",
      selector: 'html[data-power="save"] *',
      value: "40ms",
    }]);
  });
});

describe("reduced-motion transition duration normalization", () => {
  it("accepts an equivalent duration expressed in seconds", () => {
    const fixture = parse(`
      @media (prefers-reduced-motion: reduce) {
        *,
        *::before,
        *::after {
          transition-duration: .00001s !important;
        }
      }
    `);

    expect(reducedMotionTransitionDurations(fixture)).toEqual([{
      durationMs: 0.01,
      important: true,
    }]);
  });
});

describe("global CSS motion and decoration guardrails", () => {
  it("does not add ambient shell glow layers", () => {
    expect(css).not.toContain(".app-shell-bg::after");
  });

  it("does not animate login background ambient decoration", () => {
    expect(css).not.toContain("aurora-drift");
  });

  it("keeps card lift transitions complete at the opt-in utility", () => {
    const cardLiftBlock = css.match(/\.card-lift\s*\{(?<body>[\s\S]*?)\n\s*\}/)?.groups?.body ?? "";

    expect(cardLiftBlock).toContain("transform");
    expect(cardLiftBlock).toContain("box-shadow");
    expect(cardLiftBlock).toContain("border-color");
    expect(cardLiftBlock).toContain("background-color");
  });

  it("limits every power-save transition shortening to no-preference motion", () => {
    const transitions = powerSaveTransitionShortenings(root);

    expect(transitions.length).toBeGreaterThan(0);
    expect(transitions.every(({ media }) => (
      media.includes("(prefers-reduced-motion: no-preference)")
    ))).toBe(true);
  });

  it("keeps the intended 80ms power-save transition duration", () => {
    const transitions = powerSaveTransitionShortenings(root)
      .filter(({ property, value }) => {
        const durationMs = durationMilliseconds(value);
        return property === "transition-duration"
          && durationMs !== null
          && Math.abs(durationMs - 80) <= 0.000_001;
      })
      .map(({ value, ...transition }) => ({
        ...transition,
        durationMs: durationMilliseconds(value),
      }));

    expect(transitions).toEqual([{
      durationMs: 80,
      important: true,
      media: ["(prefers-reduced-motion: no-preference)"],
      property: "transition-duration",
      selector: 'html[data-power="save"] *',
    }]);
  });

  it("keeps the reduced-motion transition duration effectively disabled", () => {
    expect(reducedMotionTransitionDurations(root)).toEqual([{
      durationMs: 0.01,
      important: true,
    }]);
  });
});
