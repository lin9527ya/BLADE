"use client";

import {
	Loader2Icon,
	RefreshCwIcon,
	ShieldAlertIcon,
	Trash2Icon,
} from "lucide-react";
import * as React from "react";
import { toast } from "sonner";

import { StatusBadge } from "@/components/status-badge";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "@/components/ui/dialog";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { usePolling } from "@/hooks/use-polling";
import { api } from "@/lib/api";
import type { TaskFootprint } from "@/lib/types";

// 目标侧痕迹页签：自动归纳（命令历史 + opsec 知识库）+ Agent 补录 + 可选清理。
// 清理按用户决策支持日志/审计类修改，但必须逐条选择、二次确认、证据与审批留痕。

const CLEANABLE = new Set(["open", "unknown", "accepted"]);

// 来源节点的类型词与探索链路图保持一致（那边是图例里的 label）。
const SOURCE_KIND_LABEL: Record<string, string> = {
  intent: "意图",
  fact: "事实",
  finding: "漏洞",
  foothold: "据点",
  credential: "凭据",
  artifact: "遗留物",
  detection: "检测",
  goal: "目标",
};

function sourceKindLabel(kind: string): string {
  return SOURCE_KIND_LABEL[kind] ?? kind;
}

function clip(s: string, max: number): string {
  return s.length > max ? `${s.slice(0, max)}…` : s;
}
const STATE_OPTIONS = [
	{ value: "all", label: "全部状态" },
	{ value: "open", label: "未处置" },
	{ value: "unknown", label: "未确认" },
	{ value: "accepted", label: "接受风险" },
	{ value: "cleaned", label: "已清理" },
	{ value: "not_found", label: "未发现" },
];
const SOURCE_OPTIONS = [
	{ value: "all", label: "全部来源" },
	{ value: "predicted", label: "自动归纳" },
	{ value: "observed", label: "Agent 上报" },
];

export function FootprintsTab({ taskId }: { taskId: string }) {
	const [rows, setRows] = React.useState<TaskFootprint[]>([]);
	const [counts, setCounts] = React.useState<Record<string, number>>({});
	const [loading, setLoading] = React.useState(false);
	const [scanning, setScanning] = React.useState(false);
	const [scanNote, setScanNote] = React.useState("");
	const [stateFilter, setStateFilter] = React.useState("all");
	const [sourceFilter, setSourceFilter] = React.useState("all");
	const [selected, setSelected] = React.useState<Set<string>>(() => new Set());
	const [clearTarget, setClearTarget] = React.useState<TaskFootprint | null>(
		null,
	);
	const [acceptTarget, setAcceptTarget] = React.useState<TaskFootprint | null>(
		null,
	);
	const [notFoundTarget, setNotFoundTarget] =
		React.useState<TaskFootprint | null>(null);
	const [evidence, setEvidence] = React.useState("");
	const [reason, setReason] = React.useState("");
	const [busy, setBusy] = React.useState(false);
	const [error, setError] = React.useState("");
	const [cleanupOpen, setCleanupOpen] = React.useState(false);
	const [authorized, setAuthorized] = React.useState(false);
	const scannedRef = React.useRef(false);

	const load = React.useCallback(() => {
		setLoading(true);
		return api
			.taskFootprints(taskId)
			.then((r) => {
				setRows(r.footprints);
				setCounts(r.counts);
				setSelected((prev) => {
					if (prev.size === 0) return prev;
					const live = new Set(
						r.footprints.filter((f) => CLEANABLE.has(f.state)).map((f) => f.id),
					);
					const next = new Set([...prev].filter((id) => live.has(id)));
					return next.size === prev.size ? prev : next;
				});
			})
			.catch((e: Error) => {
				toast.error(`加载痕迹失败：${e.message}`);
			})
			.finally(() => setLoading(false));
	}, [taskId]);

	const scan = React.useCallback(
		async (showToast = false) => {
			setScanning(true);
			try {
				const r = await api.scanTaskFootprints(taskId);
				const msg = `扫描 ${r.commands_scanned} 条命令：新增 ${r.created}、更新 ${r.updated}、复发 ${r.reopened}、未收录 ${r.uncovered}`;
				setScanNote(msg);
				if (showToast) toast.success(msg);
				await load();
			} catch (e) {
				if (showToast) toast.error(`归纳失败：${(e as Error).message}`);
			} finally {
				setScanning(false);
			}
		},
		[taskId, load],
	);

	React.useEffect(() => {
		if (scannedRef.current) return;
		scannedRef.current = true;
		void scan(false);
	}, [scan]);

	usePolling(
		() => {
			void load();
		},
		5000,
		[load],
	);

	const filtered = React.useMemo(
		() =>
			rows.filter(
				(f) =>
					(stateFilter === "all" || f.state === stateFilter) &&
					(sourceFilter === "all" || f.source === sourceFilter),
			),
		[rows, stateFilter, sourceFilter],
	);
	const pageCleanable = filtered.filter((f) => CLEANABLE.has(f.state));
	const selectedCleanable = [...selected].filter((id) =>
		rows.some((f) => f.id === id && CLEANABLE.has(f.state)),
	);
	const allPageSelected =
		pageCleanable.length > 0 && pageCleanable.every((f) => selected.has(f.id));

	const toggleOne = (id: string, checked: boolean) =>
		setSelected((prev) => {
			const next = new Set(prev);
			if (checked) next.add(id);
			else next.delete(id);
			return next;
		});
	const togglePage = (checked: boolean) =>
		setSelected((prev) => {
			const next = new Set(prev);
			for (const f of pageCleanable) {
				if (checked) next.add(f.id);
				else next.delete(f.id);
			}
			return next;
		});

	const openDialog = (
		kind: "clear" | "accept" | "notfound",
		f: TaskFootprint,
	) => {
		setEvidence("");
		setReason("");
		setError("");
		if (kind === "clear") setClearTarget(f);
		if (kind === "accept") setAcceptTarget(f);
		if (kind === "notfound") setNotFoundTarget(f);
	};

	async function submitAction(kind: "clear" | "accept" | "notfound") {
		const target =
			kind === "clear"
				? clearTarget
				: kind === "accept"
					? acceptTarget
					: notFoundTarget;
		if (!target || busy) return;
		setBusy(true);
		setError("");
		try {
			if (kind === "clear")
				await api.clearTaskFootprint(taskId, target.id, evidence.trim());
			else if (kind === "accept")
				await api.acceptTaskFootprintRisk(taskId, target.id, reason.trim());
			else
				await api.markTaskFootprintNotFound(taskId, target.id, evidence.trim());
			toast.success(
				kind === "clear"
					? "已标记清理"
					: kind === "accept"
						? "已接受风险"
						: "已标记未发现",
			);
			setClearTarget(null);
			setAcceptTarget(null);
			setNotFoundTarget(null);
			await load();
		} catch (e) {
			setError((e as Error).message);
		} finally {
			setBusy(false);
		}
	}

	async function submitCleanup() {
		if (selectedCleanable.length === 0 || !authorized || busy) return;
		setBusy(true);
		setError("");
		try {
			const r = await api.cleanupTaskFootprints(taskId, selectedCleanable);
			const tail = r.skipped ? `；${r.skipped} 条已有清理意图在跑，已跳过` : "";
			toast.success(
				r.paused
					? `已创建 ${r.intents.length} 条清理意图；任务处于暂停，继续后执行${tail}`
					: `已创建 ${r.intents.length} 条清理意图，等待 worker 执行${tail}`,
			);
			setCleanupOpen(false);
			setAuthorized(false);
			setSelected(new Set());
			await load();
		} catch (e) {
			setError((e as Error).message);
		} finally {
			setBusy(false);
		}
	}

	const open = counts.open ?? 0;
	const unknown = counts.unknown ?? 0;

	return (
		<div className="flex flex-col gap-4">
			<Card>
				<CardHeader className="flex-row flex-wrap items-center justify-between gap-2">
					<CardTitle className="flex items-center gap-2 text-sm">
						<ShieldAlertIcon className="size-4" /> 目标侧痕迹
					</CardTitle>
					<div className="flex flex-wrap items-center gap-2">
						<Badge variant={open + unknown > 0 ? "destructive" : "secondary"}>
							未处置/未确认 {open + unknown}
						</Badge>
						<Badge variant="outline">已清理 {counts.cleaned ?? 0}</Badge>
						<Badge variant="outline">接受风险 {counts.accepted ?? 0}</Badge>
						<Button
							size="sm"
							variant="outline"
							disabled={scanning}
							onClick={() => void scan(true)}
						>
							{scanning ? (
								<Loader2Icon className="size-4 animate-spin" />
							) : (
								<RefreshCwIcon className="size-4" />
							)}
							重新归纳
						</Button>
					</div>
				</CardHeader>
				<CardContent className="flex flex-col gap-3">
					<p className="text-muted-foreground text-xs">
						自动归纳来自实际执行过的命令 + 可检出性知识库，是
						<strong>预期不是实测</strong>；被平台拦截的命令不计入，
						知识库未收录也不代表没有留痕（可由 worker 用 report_footprint
						补录）。{scanNote && ` ${scanNote}`}
					</p>
					<div className="flex flex-wrap items-center gap-2">
						<Select value={stateFilter} onValueChange={setStateFilter}>
							<SelectTrigger className="w-32">
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								{STATE_OPTIONS.map((o) => (
									<SelectItem key={o.value} value={o.value}>
										{o.label}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
						<Select value={sourceFilter} onValueChange={setSourceFilter}>
							<SelectTrigger className="w-32">
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								{SOURCE_OPTIONS.map((o) => (
									<SelectItem key={o.value} value={o.value}>
										{o.label}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
						<span className="text-muted-foreground text-xs tabular-nums">
							{filtered.length} 条
						</span>
						{selectedCleanable.length > 0 && (
							<>
								<Button
									size="sm"
									variant="ghost"
									onClick={() => setSelected(new Set())}
								>
									取消选择
								</Button>
								<Button
									size="sm"
									variant="destructive"
									onClick={() => {
										setError("");
										setAuthorized(false);
										setCleanupOpen(true);
									}}
								>
									<Trash2Icon data-icon="inline-start" /> 清理选中{" "}
									{selectedCleanable.length} 条
								</Button>
							</>
						)}
					</div>
					{loading && rows.length === 0 ? (
						<div className="flex items-center justify-center gap-2 py-12 text-muted-foreground text-sm">
							<Loader2Icon className="size-4 animate-spin" /> 加载中…
						</div>
					) : filtered.length === 0 ? (
						<div className="rounded-md border border-dashed py-12 text-center text-muted-foreground text-sm">
							暂无痕迹。自动归纳只覆盖命令类工具；没有结果不代表目标没有日志。
						</div>
					) : (
						<div className="flex flex-col gap-1.5">
							<div className="text-muted-foreground flex items-center gap-2 text-xs">
								<Checkbox
									checked={
										allPageSelected
											? true
											: pageCleanable.some((f) => selected.has(f.id))
												? "indeterminate"
												: false
									}
									onCheckedChange={(v) => togglePage(v === true)}
									aria-label="选择当前筛选下全部未处置痕迹"
								/>
								全选当前可清理项
							</div>
							{filtered.map((f) => {
								const selectable = CLEANABLE.has(f.state);
								return (
									<div
										key={f.id}
										className={`rounded-md border px-2.5 py-2 text-sm ${f.state === "cleaned" || f.state === "not_found" ? "opacity-70" : ""}`}
									>
										<div className="flex min-w-0 items-start gap-2">
											<Checkbox
												className="mt-0.5"
												checked={selected.has(f.id)}
												disabled={!selectable}
												onCheckedChange={(v) => toggleOne(f.id, v === true)}
												aria-label={`选择痕迹 ${f.id}`}
											/>
											<div className="min-w-0 flex-1">
												<div className="flex flex-wrap items-center gap-1.5">
													<span className="font-medium break-words">
														{f.summary}
													</span>
													<StatusBadge
														domain="footprint"
														value={f.state}
														className="px-1.5 text-[10px]"
													/>
													<Badge variant="outline" className="text-[10px]">
														{f.source === "predicted"
															? "自动推测"
															: "Agent 上报"}
													</Badge>
													{f.exposure && (
														<Badge variant="secondary" className="text-[10px]">
															{f.exposure_label || f.exposure}暴露
														</Badge>
													)}
													{f.occurrences && f.occurrences > 1 && (
														<Badge variant="outline" className="text-[10px]">
															×{f.occurrences}
														</Badge>
													)}
												</div>
												<div className="text-muted-foreground mt-0.5 flex flex-wrap gap-x-3 gap-y-0.5 text-xs">
													{f.category_label && (
														<span>分类：{f.category_label}</span>
													)}
													{f.host && <span>主机：{f.host}</span>}
													{f.locator && <span>位置：{f.locator}</span>}
													{f.where?.length ? (
														<span>留痕：{f.where.join("、")}</span>
													) : null}
												</div>
												{f.signals?.length ? (
													<div className="text-muted-foreground mt-0.5 text-xs">
														信号：{f.signals.join("；")}
													</div>
												) : null}
												{f.sources?.length ? (
													<div className="text-muted-foreground mt-0.5 text-xs">
														来自：
														{f.sources.map((src, i) => (
															<span key={src.id}>
																{i > 0 && "、"}
																<span className="text-foreground/80">
																	{sourceKindLabel(src.kind)} #{src.id}
																</span>
																{src.summary ? ` ${clip(src.summary, 40)}` : ""}
															</span>
														))}
													</div>
												) : null}
												{f.cleanup && (
													<div className="text-muted-foreground mt-0.5 text-xs">
														建议清理：{f.cleanup}
													</div>
												)}
												{f.removed_evidence && (
													<div className="mt-0.5 text-xs text-emerald-600 dark:text-emerald-400">
														清理证据：{f.removed_evidence}
													</div>
												)}
												{f.not_found_evidence && (
													<div className="mt-0.5 text-xs text-muted-foreground">
														核实证据：{f.not_found_evidence}
													</div>
												)}
												{f.accepted_reason && (
													<div className="mt-0.5 text-xs text-amber-600 dark:text-amber-400">
														接受风险：{f.accepted_reason}
													</div>
												)}
												{f.evidence && (
													<div className="text-muted-foreground mt-0.5 break-all text-xs">
														观察证据：{f.evidence}
													</div>
												)}
												{f.cleanup_intent_id ? (
													<div className="text-muted-foreground mt-0.5 text-xs">
														已排入清理意图 #{f.cleanup_intent_id}
													</div>
												) : null}
											</div>
											<div className="flex shrink-0 flex-col gap-1">
												{selectable && (
													<>
														<Button
															size="sm"
															variant="ghost"
															className="h-7 px-1.5 text-xs"
															onClick={() => openDialog("clear", f)}
														>
															标记已清理
														</Button>
														<Button
															size="sm"
															variant="ghost"
															className="h-7 px-1.5 text-xs"
															onClick={() => openDialog("accept", f)}
														>
															接受风险
														</Button>
														<Button
															size="sm"
															variant="ghost"
															className="h-7 px-1.5 text-xs"
															onClick={() => openDialog("notfound", f)}
														>
															标记未发现
														</Button>
													</>
												)}
											</div>
										</div>
									</div>
								);
							})}
						</div>
					)}
				</CardContent>
			</Card>

			<ActionDialog
				open={clearTarget !== null}
				title="标记痕迹已清理"
				description="清理证据必填：清理命令 + 关键输出。日志/审计类请写清改了哪条记录/时间窗。"
				value={evidence}
				onChange={setEvidence}
				placeholder="例如：cd /var/www/html && rm -f upload.php → 回显已删除；或 wevtutil 清除指定事件后的查询结果"
				busy={busy}
				error={error}
				onClose={() => !busy && setClearTarget(null)}
				onSubmit={() => void submitAction("clear")}
				submitLabel="确认已清理"
			/>
			<ActionDialog
				open={notFoundTarget !== null}
				title="标记痕迹未发现"
				description="核实后确认痕迹不存在：写清在哪查了、看到了什么。它不同于“已清理”。"
				value={evidence}
				onChange={setEvidence}
				placeholder="例如：grep 目标时间窗的 auth.log，无对应来源 IP 记录"
				busy={busy}
				error={error}
				onClose={() => !busy && setNotFoundTarget(null)}
				onSubmit={() => void submitAction("notfound")}
				submitLabel="确认未发现"
			/>
			<Dialog
				open={acceptTarget !== null}
				onOpenChange={(v) => !v && !busy && setAcceptTarget(null)}
			>
				<DialogContent className="max-w-md">
					<DialogHeader>
						<DialogTitle>接受痕迹风险</DialogTitle>
						<DialogDescription>
							决定不清理/清不了这条痕迹，理由必填，会写进报告。
						</DialogDescription>
					</DialogHeader>
					<Textarea
						value={reason}
						onChange={(e) => setReason(e.target.value)}
						rows={3}
						placeholder="例如：日志轮转不可控，客户确认接受；清理会破坏更重要的调查证据"
					/>
					{error && <p className="text-destructive text-sm">{error}</p>}
					<DialogFooter>
						<Button
							variant="outline"
							disabled={busy}
							onClick={() => setAcceptTarget(null)}
						>
							取消
						</Button>
						<Button
							variant="destructive"
							disabled={busy || !reason.trim()}
							onClick={() => void submitAction("accept")}
						>
							确认接受风险
						</Button>
					</DialogFooter>
				</DialogContent>
			</Dialog>

			<Dialog
				open={cleanupOpen}
				onOpenChange={(v) => !v && !busy && setCleanupOpen(false)}
			>
				<DialogContent className="max-w-lg">
					<DialogHeader>
						<DialogTitle>
							交给 Agent 清理 {selectedCleanable.length} 条痕迹
						</DialogTitle>
						<DialogDescription>
							将创建清理意图，由 worker
							在授权范围与拦截审批下执行；命令仍可能被规则拦截或要求人工批准。
							若任务已完成/暂停，会被恢复运行（暂停任务需手动继续）。
						</DialogDescription>
					</DialogHeader>
					<div className="max-h-48 overflow-auto rounded-md border bg-muted/20 p-2 text-xs">
						{rows
							.filter((f) => selected.has(f.id))
							.map((f) => (
								<div
									key={f.id}
									className="truncate"
									title={f.cleanup || f.summary}
								>
									#{f.id} {f.summary}
									{f.cleanup ? ` —— ${f.cleanup}` : ""}
								</div>
							))}
					</div>
					<div className="flex items-start gap-2 text-sm">
						<Checkbox
							checked={authorized}
							onCheckedChange={(v) => setAuthorized(v === true)}
							className="mt-0.5"
							aria-label="确认已获授权并理解日志修改风险"
						/>
						<span>
							我确认已获授权，并理解日志/审计类修改可能破坏目标侧证据链、触发告警；平台侧操作与证据仍会留档。
						</span>
					</div>
					{error && <p className="text-destructive text-sm">{error}</p>}
					<DialogFooter>
						<Button
							variant="outline"
							disabled={busy}
							onClick={() => setCleanupOpen(false)}
						>
							取消
						</Button>
						<Button
							variant="destructive"
							disabled={busy || !authorized}
							onClick={() => void submitCleanup()}
						>
							{busy ? "提交中…" : "确认提交清理"}
						</Button>
					</DialogFooter>
				</DialogContent>
			</Dialog>
		</div>
	);
}

function ActionDialog({
	open,
	title,
	description,
	value,
	onChange,
	placeholder,
	busy,
	error,
	onClose,
	onSubmit,
	submitLabel,
}: {
	open: boolean;
	title: string;
	description: string;
	value: string;
	onChange: (v: string) => void;
	placeholder: string;
	busy: boolean;
	error: string;
	onClose: () => void;
	onSubmit: () => void;
	submitLabel: string;
}) {
	return (
		<Dialog open={open} onOpenChange={(v) => !v && onClose()}>
			<DialogContent className="max-w-md">
				<DialogHeader>
					<DialogTitle>{title}</DialogTitle>
					<DialogDescription>{description}</DialogDescription>
				</DialogHeader>
				<Textarea
					value={value}
					onChange={(e) => onChange(e.target.value)}
					rows={4}
					placeholder={placeholder}
				/>
				{error && <p className="text-destructive text-sm">{error}</p>}
				<DialogFooter>
					<Button variant="outline" disabled={busy} onClick={onClose}>
						取消
					</Button>
					<Button disabled={busy || !value.trim()} onClick={onSubmit}>
						{busy ? "提交中…" : submitLabel}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
