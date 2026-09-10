import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { appI18n } from "../i18n";
import type { UsageRequestLogPage } from "../hooks/useUsageRequestLog";
import { RequestLogTable } from "./RequestLogTable";

const useUsageRequestLogMock = vi.hoisted(() => vi.fn());
vi.mock("../hooks/useUsageRequestLog", () => ({
	usageRequestLogQueryRoot: ["usage-request-log"] as const,
	useUsageRequestLog: (...args: unknown[]) => useUsageRequestLogMock(...args),
}));

const navigateToSessionMock = vi.hoisted(() => vi.fn());
vi.mock("../lib/navigate-to-session", () => ({
	useNavigateToSession: () => navigateToSessionMock,
}));

type LogEntry = UsageRequestLogPage["items"][number];

function logEntry(overrides: Partial<LogEntry>): LogEntry {
	return {
		id: 1,
		createdAt: "2026-09-08T12:00:00Z",
		billingProviderId: "anthropic",
		modelId: "claude-sonnet",
		inputTokens: 100,
		cachedInputTokens: null,
		outputTokens: 40,
		cacheCreationInputTokens: null,
		estimatedCostNanos: 135,
		llmMs: null,
		firstTokenMs: null,
		sourceKind: "claude_main",
		sessionId: "sess-1",
		sessionExists: true,
		...overrides,
	};
}

function renderTable(items: LogEntry[]) {
	useUsageRequestLogMock.mockReturnValue({
		data: items,
		isLoading: false,
		isError: false,
		fetchNextPage: vi.fn(),
		isFetchingNextPage: false,
		hasNextPage: false,
	});
	return render(<RequestLogTable />);
}

describe("RequestLogTable timing columns", () => {
	beforeEach(async () => {
		await appI18n.changeLanguage("en");
		useUsageRequestLogMock.mockReset();
	});

	it("renders the duration and first-token headers", () => {
		renderTable([logEntry({ llmMs: 1200, firstTokenMs: 340 })]);

		expect(screen.getByRole("columnheader", { name: "Duration" })).toBeInTheDocument();
		expect(screen.getByRole("columnheader", { name: "First token" })).toBeInTheDocument();
	});

	it("formats known timing facts as human-friendly durations", () => {
		renderTable([logEntry({ llmMs: 1200, firstTokenMs: 340 })]);

		expect(screen.getByText("1.2s")).toBeInTheDocument();
		expect(screen.getByText("340ms")).toBeInTheDocument();
	});

	it("renders the unknown marker, never a zero, for rows without timing facts", () => {
		// Every non-timing cell carries a known value, so the only dashes in the
		// row are the two unknown timing cells.
		renderTable([logEntry({ llmMs: null, firstTokenMs: null })]);

		expect(screen.getAllByText("—")).toHaveLength(2);
	});

	it("mixes known and unknown timing across rows", () => {
		renderTable([
			logEntry({ id: 1, llmMs: 65_000, firstTokenMs: null }),
			logEntry({ id: 2, llmMs: null, firstTokenMs: 1_217_000 }),
		]);

		expect(screen.getByText("1m")).toBeInTheDocument();
		expect(screen.getByText("20m")).toBeInTheDocument();
		expect(screen.getAllByText("—")).toHaveLength(2);
	});
});
