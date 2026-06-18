import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";

import { describe, expect, it } from "vitest";

const currentDir = dirname(fileURLToPath(import.meta.url));
const groupsViewSource = readFileSync(
  resolve(currentDir, "../GroupsView.vue"),
  "utf8",
);

describe("groups models list layout", () => {
  it("keeps the toolbar outside of the scrolling list content", () => {
    expect(groupsViewSource).toContain("overflow-hidden rounded-lg border");
    expect(groupsViewSource).toContain("max-h-64 space-y-2 overflow-y-auto p-2");
    expect(groupsViewSource).not.toContain("sticky top-0");
  });

  it("uses the shared Select component for Kiro endpoint mode fields", () => {
    const endpointModeSections = groupsViewSource
      .split('t("admin.groups.kiroCache.endpointMode")')
      .slice(1)
      .map((section) =>
        section.slice(
          0,
          section.indexOf('t("admin.groups.kiroCache.endpointModeHint")'),
        ),
      );

    expect(endpointModeSections).toHaveLength(2);
    expect(groupsViewSource).toContain("const kiroEndpointModeOptions = computed(() => [");

    for (const section of endpointModeSections) {
      expect(section).toContain("<Select");
      expect(section).toContain(':options="kiroEndpointModeOptions"');
      expect(section).not.toContain("<select");
    }
  });
});
