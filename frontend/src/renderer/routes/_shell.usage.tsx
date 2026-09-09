import { createFileRoute } from "@tanstack/react-router";
import { UsageStatisticsView } from "../components/UsageStatisticsView";

export const Route = createFileRoute("/_shell/usage")({
	component: UsageStatisticsView,
});
