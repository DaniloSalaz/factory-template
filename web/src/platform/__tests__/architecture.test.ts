// @vitest-environment node
import { existsSync, readdirSync, readFileSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";
import { describe, expect, it } from "vitest";

// [platform-imports-app] src/platform is template code: apps never edit it, so it must
// not depend on them. It reads the app only through app-registry.ts.
const src = resolve(import.meta.dirname, "../..");
const allowed = "platform/app-registry.ts";
const self = "platform/__tests__/architecture.test.ts"; // its fixtures name src/app on purpose

const specifiers = (code: string): string[] =>
  [...code.matchAll(/(?:\bfrom\s*|\bimport\s*\(?\s*)["']([^"']+)["']/g)].flatMap((m) =>
    m[1] ? [m[1]] : [],
  );

/** resolveSpecifier gives the path under src/ that an import names, or null for packages. */
function resolveSpecifier(file: string, spec: string): string | null {
  if (spec.startsWith("@/")) {
    return spec.slice(2);
  }
  if (spec.startsWith(".")) {
    return relative(src, resolve(dirname(join(src, file)), spec));
  }
  return null;
}

function appImports(file: string, code: string): string[] {
  if (file === allowed) {
    return [];
  }
  return specifiers(code).filter((s) => {
    const target = resolveSpecifier(file, s);
    return target !== null && (target === "app" || target.startsWith("app/"));
  });
}

function filesUnder(dir: string): string[] {
  if (!existsSync(join(src, dir))) {
    return [];
  }
  return readdirSync(join(src, dir), { withFileTypes: true, recursive: true })
    .filter((e) => e.isFile() && /\.(ts|tsx|css)$/.test(e.name))
    .map((e) => relative(src, join(e.parentPath, e.name)));
}

describe("[platform-imports-app]", () => {
  it("platform imports src/app only in app-registry.ts", () => {
    const offenders = filesUnder("platform")
      .filter((f) => f !== self)
      .flatMap((f) => appImports(f, readFileSync(join(src, f), "utf8")).map((s) => `${f}: ${s}`));
    expect(
      offenders,
      "src/platform must not import src/app; register what the shell needs in src/app/registry.ts",
    ).toEqual([]);
  });

  it("catches every way of naming src/app", () => {
    const f = "platform/routes/x.tsx";
    expect(appImports(f, 'import { a } from "@/app/registry";')).toHaveLength(1);
    expect(appImports(f, 'import { a } from "../../app/texts";')).toHaveLength(1);
    expect(appImports(f, 'import "../../app/theme.css";')).toHaveLength(1);
    expect(appImports(f, 'const m = await import("@/app/routes/example");')).toHaveLength(1);
    expect(appImports(f, 'export { x } from "../../app";')).toHaveLength(1);
    expect(appImports(f, 'import { a } from "@/platform/app-types";')).toHaveLength(0);
    expect(appImports(f, 'import { a } from "./app-like";')).toHaveLength(0);
    expect(appImports(allowed, 'import { app } from "../app/registry";')).toHaveLength(0);
  });
});

// [feature-boundaries] Each folder of src/app/features is one feature (named as its module
// in api/app/modules): it does not reach into another one, so a feature can change or go
// away alone. What two features share goes in src/app/shared.
const features = "app/features/";

/** featureOf is the feature a path under src/ belongs to, "" for its barrel, or null. */
function featureOf(path: string): string | null {
  if (path === "app/features") {
    return "";
  }
  if (!path.startsWith(features)) {
    return null;
  }
  return path.slice(features.length).split("/")[0] ?? "";
}

function crossFeatureImports(file: string, code: string): string[] {
  const own = featureOf(file);
  if (own === null) {
    return [];
  }
  return specifiers(code).filter((s) => {
    const target = resolveSpecifier(file, s);
    const other = target === null ? null : featureOf(target);
    return other !== null && other !== own;
  });
}

describe("[feature-boundaries]", () => {
  it("a feature of src/app/features imports no other feature", () => {
    const offenders = filesUnder("app/features").flatMap((f) =>
      crossFeatureImports(f, readFileSync(join(src, f), "utf8")).map((s) => `${f}: ${s}`),
    );
    expect(
      offenders,
      "[feature-boundaries] a file of src/app/features/<a> must not import src/app/features/<b>; move what both need to src/app/shared/",
    ).toEqual([]);
  });

  it("catches every way of naming another feature", () => {
    const f = "app/features/example/pages/ExamplePage.tsx";
    expect(crossFeatureImports(f, 'import { a } from "@/app/features/otra/x";')).toHaveLength(1);
    expect(crossFeatureImports(f, 'import { a } from "../../otra/hooks/y";')).toHaveLength(1);
    expect(crossFeatureImports(f, 'const m = await import("@/app/features/otra");')).toHaveLength(
      1,
    );
    expect(crossFeatureImports(f, 'export { x } from "../..";')).toHaveLength(1);
    expect(crossFeatureImports(f, 'import "../../otra/styles.css";')).toHaveLength(1);
    expect(crossFeatureImports(f, 'import { a } from "@/app/features/example-2/x";')).toHaveLength(
      1,
    );
    expect(crossFeatureImports(f, 'import { a } from "../components/Card";')).toHaveLength(0);
    expect(
      crossFeatureImports(f, 'import { a } from "@/app/features/example/hooks/y";'),
    ).toHaveLength(0);
    expect(crossFeatureImports(f, 'import { a } from "@/app/shared/z";')).toHaveLength(0);
    expect(crossFeatureImports(f, 'import { a } from "@/platform/ui/button";')).toHaveLength(0);
    expect(
      crossFeatureImports("app/routes/example.tsx", 'import { a } from "@/app/features/otra/x";'),
    ).toHaveLength(0);
  });
});
