import { describe, it, expect } from "vitest";
import { buildOverrides } from "./build-overrides";

describe("buildOverrides", () => {
  it("emits allowed paths into a nested structure", () => {
    const out = buildOverrides(
      ["budget.monthlyUsd", "platform.persona"],
      [
        ["budget.monthlyUsd", 500],
        ["platform.persona", "eng"],
      ],
    );
    expect(out).toEqual({
      budget: { monthlyUsd: 500 },
      platform: { persona: "eng" },
    });
  });

  it("drops paths that are not in the allowlist", () => {
    const out = buildOverrides(
      ["budget.monthlyUsd"],
      [
        ["budget.monthlyUsd", 500],
        ["platform.persona", "eng"], // locked → dropped
        ["platform.compliance.soc2", true], // locked → dropped
      ],
    );
    expect(out).toEqual({ budget: { monthlyUsd: 500 } });
    expect("platform" in out).toBe(false);
  });

  it("merges several allowed paths into a shared parent", () => {
    const out = buildOverrides(
      [
        "platform.persona",
        "platform.displayName",
        "platform.compliance.soc2",
        "platform.compliance.hipaa",
      ],
      [
        ["platform.persona", "eng"],
        ["platform.displayName", "Eng Team"],
        ["platform.compliance.soc2", true],
        ["platform.compliance.hipaa", false],
      ],
    );
    expect(out).toEqual({
      platform: {
        persona: "eng",
        displayName: "Eng Team",
        compliance: { soc2: true, hipaa: false },
      },
    });
  });

  it("builds null-prototype objects at every level", () => {
    const out = buildOverrides(
      ["platform.compliance.soc2"],
      [["platform.compliance.soc2", true]],
    );
    expect(Object.getPrototypeOf(out)).toBeNull();
    const platform = out.platform as object;
    expect(Object.getPrototypeOf(platform)).toBeNull();
    const compliance = (platform as Record<string, unknown>).compliance as object;
    expect(Object.getPrototypeOf(compliance)).toBeNull();
  });

  it("rejects a __proto__ segment even when the path is allowlisted", () => {
    const out = buildOverrides(
      ["__proto__.polluted", "platform.persona"],
      [
        ["__proto__.polluted", "yes"],
        ["platform.persona", "eng"],
      ],
    );
    expect(out).toEqual({ platform: { persona: "eng" } });
    // Object.prototype is untouched.
    expect(({} as Record<string, unknown>).polluted).toBeUndefined();
  });

  it("rejects constructor and prototype segments", () => {
    const out = buildOverrides(
      ["constructor.prototype.polluted", "a.prototype.b", "platform.persona"],
      [
        ["constructor.prototype.polluted", "x"],
        ["a.prototype.b", "y"],
        ["platform.persona", "eng"],
      ],
    );
    expect(out).toEqual({ platform: { persona: "eng" } });
    expect(({} as Record<string, unknown>).polluted).toBeUndefined();
  });

  it("returns an empty object when nothing is allowed", () => {
    const out = buildOverrides([], [["budget.monthlyUsd", 500]]);
    expect(Object.keys(out)).toHaveLength(0);
  });
});
