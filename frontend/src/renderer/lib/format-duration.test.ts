import { describe, expect, it } from "vitest";
import { formatDurationMs } from "./format-duration";

describe("formatDurationMs", () => {
	it("returns null for unknown values so callers render the unknown marker", () => {
		expect(formatDurationMs(null)).toBeNull();
		expect(formatDurationMs(undefined)).toBeNull();
	});

	it("renders milliseconds under one second", () => {
		expect(formatDurationMs(0)).toBe("0ms");
		expect(formatDurationMs(340)).toBe("340ms");
		expect(formatDurationMs(999)).toBe("999ms");
	});

	it("renders seconds with one decimal, dropping the trailing .0", () => {
		expect(formatDurationMs(1000)).toBe("1s");
		expect(formatDurationMs(1200)).toBe("1.2s");
		expect(formatDurationMs(3400)).toBe("3.4s");
		expect(formatDurationMs(59_900)).toBe("59.9s");
	});

	it("renders whole minutes at and above one minute", () => {
		expect(formatDurationMs(60_000)).toBe("1m");
		expect(formatDurationMs(65_000)).toBe("1m");
		expect(formatDurationMs(1_217_000)).toBe("20m");
	});
});
