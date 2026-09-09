import { useTranslation } from "react-i18next";
import { Card } from "./ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "./ui/table";
import { formatCostNanos } from "../lib/format-cost";
import { formatTokenCount } from "../lib/format-token-count";

export type UsageAggregateRow = {
	name: string;
	attribution?: "observed" | "inferred" | "mixed" | null;
	requestCount: number;
	tokens: number | null;
	totalCostNanos: number | null;
	averageCostPerRequestNanos: number | null;
};

type UsageAggregateTableProps = {
	variant: "models" | "providers";
	rows: UsageAggregateRow[];
	isLoading?: boolean;
	isError?: boolean;
};

const count = new Intl.NumberFormat(undefined);

export function UsageAggregateTable({ variant, rows, isLoading, isError }: UsageAggregateTableProps) {
	const { t } = useTranslation();

	if (isLoading) {
		return <p className="text-sm text-muted-foreground">{t("usage.loading")}</p>;
	}
	if (isError) {
		return <p className="text-sm text-muted-foreground">{t("usage.loadFailed")}</p>;
	}
	if (rows.length === 0) {
		return <p className="text-sm text-muted-foreground">{t("usage.emptyRange")}</p>;
	}

	const nameHeader = variant === "models" ? t("usage.col.model") : t("usage.col.provider");

	return (
		<Card size="sm">
			<Table>
				<TableHeader>
					<TableRow>
						<TableHead>{nameHeader}</TableHead>
						<TableHead className="text-right">{t("usage.col.requests")}</TableHead>
						<TableHead className="text-right">{t("usage.col.tokens")}</TableHead>
						<TableHead className="text-right">{t("usage.col.totalCost")}</TableHead>
						<TableHead className="text-right">{t("usage.col.avgCost")}</TableHead>
					</TableRow>
				</TableHeader>
				<TableBody>
					{rows.map((row) => (
						<TableRow key={row.name}>
							<TableCell>
								<div className="flex items-center gap-2">
									<span>{row.name === "" ? t("usage.unattributed") : row.name}</span>
									{variant === "providers" && row.attribution === "inferred" ? (
										<span className="rounded-full bg-muted px-1.5 py-0.5 text-xs text-muted-foreground">
											{t("usage.attributionInferred")}
										</span>
									) : null}
									{variant === "providers" && row.attribution === "mixed" ? (
										<span className="rounded-full bg-muted px-1.5 py-0.5 text-xs text-muted-foreground">
											{t("usage.attributionMixed")}
										</span>
									) : null}
								</div>
							</TableCell>
							<TableCell className="text-right tabular-nums">{count.format(row.requestCount)}</TableCell>
							<TableCell className="text-right tabular-nums">
								{row.tokens == null ? "—" : formatTokenCount(row.tokens)}
							</TableCell>
							<TableCell className="text-right tabular-nums">
								{formatCostNanos(row.totalCostNanos) ?? "—"}
							</TableCell>
							<TableCell className="text-right tabular-nums">
								{formatCostNanos(row.averageCostPerRequestNanos) ?? "—"}
							</TableCell>
						</TableRow>
					))}
				</TableBody>
			</Table>
		</Card>
	);
}