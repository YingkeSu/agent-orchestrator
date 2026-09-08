import { useTranslation } from "react-i18next";
import { Button } from "./ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "./ui/table";
import { useUsageRequestLog } from "../hooks/useUsageRequestLog";
import { formatCostNanos } from "../lib/format-cost";
import { formatTokenCount } from "../lib/format-token-count";
import { useNavigateToSession } from "../lib/navigate-to-session";
import type { MessageKey } from "../i18n/messages";

type RequestLogTableProps = {
	from?: string;
	to?: string;
	source?: string;
	model?: string;
};

// Usage source kinds are the certified artifact shapes the daemon persists.
// Known kinds get a friendly localized label; unknown kinds fall back to the
// raw kind so the table still names what the data says.
const SOURCE_LABEL_KEY: Record<string, MessageKey> = {
	claude_main: "usage.log.source.claude_main",
	claude_subagent: "usage.log.source.claude_subagent",
	codex_rollout: "usage.log.source.codex_rollout",
	kimi_wire: "usage.log.source.kimi_wire",
};

function sourceLabel(t: (key: MessageKey) => string, kind: string): string {
	const key = SOURCE_LABEL_KEY[kind];
	return key ? t(key) : kind;
}

export function RequestLogTable({ from, to, source, model }: RequestLogTableProps) {
	const { t } = useTranslation();
	const navigateToSession = useNavigateToSession();
	const { data, isLoading, isError, fetchNextPage, isFetchingNextPage, hasNextPage } = useUsageRequestLog(
		from,
		to,
		source,
		model,
	);

	if (isLoading) {
		return <p className="text-sm text-muted-foreground">{t("usage.log.loading")}</p>;
	}
	if (isError || !data) {
		return <p className="text-sm text-muted-foreground">{t("usage.log.loadFailed")}</p>;
	}
	if (data.length === 0) {
		return <p className="text-sm text-muted-foreground">{t("usage.log.empty")}</p>;
	}

	return (
		<div className="flex flex-col gap-4">
			<Table>
				<TableHeader>
					<TableRow>
						<TableHead>{t("usage.log.time")}</TableHead>
						<TableHead>{t("usage.log.provider")}</TableHead>
						<TableHead>{t("usage.log.model")}</TableHead>
						<TableHead>{t("usage.log.input")}</TableHead>
						<TableHead>{t("usage.log.output")}</TableHead>
						<TableHead>{t("usage.log.cost")}</TableHead>
						<TableHead>{t("usage.log.source")}</TableHead>
						<TableHead>{t("usage.log.session")}</TableHead>
					</TableRow>
				</TableHeader>
				<TableBody>
					{data.map((entry) => {
						const input = formatTokens(entry.inputTokens);
						const cached = formatTokens(entry.cachedInputTokens);
						const inputLabel =
							input === null
								? "—"
								: cached === null
									? input
									: `${input} (+${cached})`;
						return (
							<TableRow key={entry.id}>
								<TableCell className="tabular-nums whitespace-nowrap">
									{formatTimestamp(entry.createdAt)}
								</TableCell>
								<TableCell>{entry.billingProviderId ?? "—"}</TableCell>
								<TableCell>{entry.modelId}</TableCell>
								<TableCell className="tabular-nums">{inputLabel}</TableCell>
								<TableCell className="tabular-nums">{formatTokens(entry.outputTokens) ?? "—"}</TableCell>
								<TableCell className="tabular-nums">
									{formatCostNanos(entry.estimatedCostNanos) ?? "—"}
								</TableCell>
								<TableCell>{sourceLabel(t, entry.sourceKind)}</TableCell>
								<TableCell>
									{entry.sessionExists ? (
										<Button
											variant="ghost"
											size="none"
											className="h-auto p-0 text-accent hover:bg-transparent"
											onClick={() => navigateToSession(undefined, entry.sessionId)}
										>
											{entry.sessionId}
										</Button>
									) : (
										<span className="text-muted-foreground">{entry.sessionId}</span>
									)}
								</TableCell>
							</TableRow>
						);
					})}
				</TableBody>
			</Table>
			{hasNextPage ? (
				<Button
					variant="outline"
					className="self-start"
					onClick={() => void fetchNextPage()}
					disabled={isFetchingNextPage}
				>
					{isFetchingNextPage ? t("usage.log.loadingMore") : t("usage.log.loadMore")}
				</Button>
			) : null}
		</div>
	);
}

function formatTokens(value: number | null | undefined): string | null {
	if (value === null || value === undefined) return null;
	return formatTokenCount(value);
}

function formatTimestamp(iso: string | null): string {
	if (iso === null) return "—";
	return new Intl.DateTimeFormat(undefined, {
		month: "short",
		day: "numeric",
		hour: "2-digit",
		minute: "2-digit",
	}).format(new Date(iso));
}
