import { useCallback, useEffect, useMemo, useState } from "react";
import {
  AddRegular,
  DeleteRegular,
  EditRegular,
  PlayRegular,
  SendClockRegular,
} from "@fluentui/react-icons";
import { api, apiMessage } from "../api";
import type {
  SMSTestEndpoint,
  SMSTestResult,
  SMSTestResultsResponse,
  SMSTestSchedule,
} from "../types";
import {
  Button,
  Input,
  Modal,
  PageHeader,
  Select,
  Switch,
  Tabs,
  Tag,
  Textarea,
  Tooltip,
  confirmDialog,
  message,
} from "../components/ui";
import { useI18n } from "../lib/i18n";
import { cx } from "../lib/utils";

type TabKey = "statistics" | "schedules" | "endpoints";

interface KeyValuePair {
  key: string;
  value: string;
  // hasValue marks a credential the server is holding. Its value never
  // travels to the browser, so this is all there is to show.
  hasValue?: boolean;
}

// The same names the server treats as credentials (secretParamKeys in
// internal/server/smstest_secrets.go). Duplicated rather than fetched: the
// field has to mask itself as the name is typed, and a round trip per
// keystroke to learn that would be worse than keeping two lists in step.
const SECRET_PARAM_KEYS = new Set([
  "password", "passwd", "pwd", "pass",
  "secret", "apisecret", "appsecret", "clientsecret",
  "token", "apitoken", "accesstoken", "authtoken",
  "apikey", "appkey", "accesskey", "secretkey",
  "auth", "authorization", "credential",
]);

function isSecretParamKey(key: string): boolean {
  return SECRET_PARAM_KEYS.has(key.toLowerCase().replace(/[^a-z0-9]/g, ""));
}

// A value that is nothing but {{placeholders}} is a reference, not a
// credential: the secret it names lives in the endpoint's own password field,
// which the server never returns. Masking the reference protects nothing and
// hides how the endpoint is wired -- mirrors isPlaceholderValue in
// internal/server/smstest_secrets.go.
function isPlaceholderValue(value: string): boolean {
  if (value.trim() === "") return false;
  return value.replace(/{{\s*[A-Za-z0-9_]+\s*}}/g, "").trim() === "";
}

// A pair is masked only when there is a literal secret to hide.
function isMaskedPair(pair: KeyValuePair): boolean {
  return isSecretParamKey(pair.key) && !isPlaceholderValue(pair.value);
}

const EMPTY_ENDPOINT_FORM = {
  id: "",
  name: "",
  method: "POST",
  url: "",
  username: "",
  password: "",
  headers: [] as KeyValuePair[],
  bodyParams: [] as KeyValuePair[],
};

const EMPTY_SCHEDULE_FORM = {
  id: "",
  name: "",
  endpointId: "",
  recipient: "",
  sender: "",
  contentTemplate: "Your verification code is {{code}}",
  codeType: "digits",
  codeLength: 6,
  frequencyMinutes: 60,
  startTime: "",
  enabled: true,
  isExternal: false,
};

type EndpointForm = typeof EMPTY_ENDPOINT_FORM;
type ScheduleForm = typeof EMPTY_SCHEDULE_FORM;

function parsePairs(raw: string): KeyValuePair[] {
  try {
    const parsed = JSON.parse(raw || "[]");
    if (!Array.isArray(parsed)) return [];
    return parsed
      .filter((item) => item && typeof item === "object")
      .map((item) => ({
        key: String(item.key ?? ""),
        value: String(item.value ?? ""),
        hasValue: Boolean(item.has_value),
      }));
  } catch {
    return [];
  }
}

// Key and value only: hasValue is the server's marker coming the other way,
// and sending it back would store it.
function serializePairs(pairs: KeyValuePair[]): string {
  return JSON.stringify(
    pairs
      .filter((pair) => pair.key.trim() !== "")
      .map((pair) => ({ key: pair.key, value: pair.value })),
  );
}

const STATUS_LABELS: Record<string, { zh: string; en: string }> = {
  received: { zh: "已送达", en: "Received" },
  delayed: { zh: "延迟", en: "Delayed" },
  failed: { zh: "失败", en: "Failed" },
  pending: { zh: "等待中", en: "Pending" },
  sent: { zh: "已发送", en: "Sent" },
};

function statusLabel(status: string, zh: boolean): string {
  const entry = STATUS_LABELS[status];
  if (!entry) return status;
  return zh ? entry.zh : entry.en;
}

// Delivery latency is a magnitude, so the dot strip bands it into a short
// severity scale instead of a rainbow: exact timings live in the hover
// tooltip. Colours are validated for colour-vision deficiency, and every band
// also differs in shape so the state never rests on colour alone.
type DotKind = "fast" | "slow" | "late" | "failed" | "external" | "pending";

const DOT_BANDS: {
  kind: DotKind;
  zh: string;
  en: string;
  className: string;
}[] = [
  { kind: "fast", zh: "30 秒内送达", en: "Under 30s", className: "rounded-full bg-[#16a34a] dark:bg-[#4ade80]" },
  { kind: "slow", zh: "30–60 秒", en: "30-60s", className: "rounded-full bg-[#d9a404] dark:bg-[#facc15]" },
  { kind: "late", zh: "超过 60 秒", en: "Over 60s", className: "rounded-full bg-[#dc2626] dark:bg-[#f87171]" },
  { kind: "failed", zh: "未收到 / 超时", en: "Never arrived", className: "rounded-[2px] bg-[#dc2626] dark:bg-[#f87171]" },
  { kind: "external", zh: "外部号码（仅发送）", en: "External (send only)", className: "rotate-45 rounded-[2px] bg-[#2563eb] dark:bg-[#60a5fa]" },
  { kind: "pending", zh: "等待接收", en: "Waiting", className: "animate-pulse rounded-full border-2 border-gray-500 bg-white dark:border-gray-300 dark:bg-transparent" },
];

const DOT_CLASS = new Map(DOT_BANDS.map((band) => [band.kind, band.className]));

function dotKind(result: SMSTestResult, schedule?: SMSTestSchedule): DotKind {
  if (result.status === "pending") return "pending";
  if (result.status === "failed") return "failed";
  if (result.status === "sent" || schedule?.isExternal) return "external";
  if (result.receivedAt && result.elapsedMs !== null && result.elapsedMs !== undefined) {
    if (result.elapsedMs < 30_000) return "fast";
    if (result.elapsedMs < 60_000) return "slow";
    return "late";
  }
  return "fast";
}

function statusType(status: string): "success" | "warning" | "danger" | "info" {
  switch (status) {
    case "received":
      return "success";
    case "delayed":
      return "warning";
    case "failed":
      return "danger";
    default:
      return "info";
  }
}

function formatTimestamp(value: string | null): string {
  if (!value) return "--";
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? "--" : parsed.toLocaleString();
}

function formatElapsed(result: SMSTestResult): string {
  if (result.elapsedMs === null || result.elapsedMs === undefined) return "--";
  if (result.elapsedMs < 1000) return `${result.elapsedMs} ms`;
  return `${(result.elapsedMs / 1000).toFixed(1)} s`;
}

// PairEditor edits the JSON-encoded header and body-parameter lists an
// endpoint stores. Values may contain {{username}}, {{password}}, {{to}},
// {{from}} and {{content}} placeholders.
function PairEditor({
  label,
  pairs,
  onChange,
}: {
  label: string;
  pairs: KeyValuePair[];
  onChange: (pairs: KeyValuePair[]) => void;
}) {
  const { t } = useI18n();
  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between">
        <span className="text-sm font-medium text-gray-700 dark:text-gray-300">{label}</span>
        <Button
          size="small"
          variant="text"
          onClick={() => onChange([...pairs, { key: "", value: "" }])}
        >
          <AddRegular className="text-[16px]" />
          {t("添加")}
        </Button>
      </div>
      {pairs.length === 0 ? (
        <p className="text-xs text-gray-400 dark:text-gray-500">{t("暂无条目")}</p>
      ) : (
        pairs.map((pair, index) => (
          <div key={index} className="flex items-center gap-2">
            <Input
              value={pair.key}
              placeholder={t("名称")}
              onChange={(event) => {
                const next = [...pairs];
                next[index] = { ...next[index], key: event.target.value };
                onChange(next);
              }}
            />
            {/* A parameter named like a credential is write-only: the server
                never returns its value, and saving it blank keeps what is
                stored. */}
            <Input
              value={pair.value}
              type={isMaskedPair(pair) ? "password" : "text"}
              placeholder={
                isMaskedPair(pair)
                  ? pair.hasValue
                    ? t("已设置，留空则不修改")
                    : t("值（保存后不再回显）")
                  : t("值")
              }
              onChange={(event) => {
                const next = [...pairs];
                next[index] = { ...next[index], value: event.target.value };
                onChange(next);
              }}
            />
            <Button
              size="small"
              variant="text"
              onClick={() => onChange(pairs.filter((_, position) => position !== index))}
            >
              <DeleteRegular className="text-[16px]" />
            </Button>
          </div>
        ))
      )}
    </div>
  );
}

export default function SmsTestPage() {
  const { t, lang } = useI18n();
  const zh = lang === "zh";
  const [tab, setTab] = useState<TabKey>("statistics");

  const [endpoints, setEndpoints] = useState<SMSTestEndpoint[]>([]);
  const [schedules, setSchedules] = useState<SMSTestSchedule[]>([]);
  const [results, setResults] = useState<SMSTestResult[]>([]);
  const [summary, setSummary] = useState<Record<string, number>>({});
  const [hours, setHours] = useState(24);
  const [detailView, setDetailView] = useState(false);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);

  const [endpointForm, setEndpointForm] = useState<EndpointForm>(EMPTY_ENDPOINT_FORM);
  const [endpointOpen, setEndpointOpen] = useState(false);
  const [scheduleForm, setScheduleForm] = useState<ScheduleForm>(EMPTY_SCHEDULE_FORM);
  const [scheduleOpen, setScheduleOpen] = useState(false);

  const loadEndpoints = useCallback(async () => {
    try {
      const data = await api<{ endpoints: SMSTestEndpoint[] }>("/smstest/endpoints");
      setEndpoints(data.endpoints ?? []);
    } catch (error) {
      message.error(apiMessage(error) || t("接口配置加载失败"));
    }
  }, [t]);

  const loadSchedules = useCallback(async () => {
    try {
      const data = await api<{ schedules: SMSTestSchedule[] }>("/smstest/schedules");
      setSchedules(data.schedules ?? []);
    } catch (error) {
      message.error(apiMessage(error) || t("测试计划加载失败"));
    }
  }, [t]);

  const loadResults = useCallback(async () => {
    try {
      const data = await api<SMSTestResultsResponse>(`/smstest/results?hours=${hours}`);
      setResults(data.results ?? []);
      setSummary(data.summary ?? {});
    } catch (error) {
      message.error(apiMessage(error) || t("测试结果加载失败"));
    }
  }, [hours, t]);

  const refresh = useCallback(async () => {
    setLoading(true);
    try {
      await Promise.all([loadEndpoints(), loadSchedules(), loadResults()]);
    } finally {
      setLoading(false);
    }
  }, [loadEndpoints, loadSchedules, loadResults]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const endpointOptions = useMemo(
    () => endpoints.map((endpoint) => ({ value: endpoint.id, label: endpoint.name || endpoint.id })),
    [endpoints],
  );
  const endpointNames = useMemo(() => {
    const names = new Map<string, string>();
    for (const endpoint of endpoints) names.set(endpoint.id, endpoint.name || endpoint.id);
    return names;
  }, [endpoints]);
  const scheduleNames = useMemo(() => {
    const names = new Map<string, string>();
    for (const schedule of schedules) names.set(schedule.id, schedule.name || schedule.id);
    return names;
  }, [schedules]);

  // Results arrive newest first; the dot strip reads left to right as time
  // moving forward, so each schedule's row is reversed into oldest-first.
  const resultsBySchedule = useMemo(() => {
    const grouped = new Map<string, SMSTestResult[]>();
    for (const result of results) {
      const bucket = grouped.get(result.scheduleId);
      if (bucket) bucket.push(result);
      else grouped.set(result.scheduleId, [result]);
    }
    for (const bucket of grouped.values()) {
      bucket.sort((left, right) => left.sentAt.localeCompare(right.sentAt));
    }
    return grouped;
  }, [results]);

  function openEndpointEditor(endpoint?: SMSTestEndpoint) {
    if (!endpoint) {
      setEndpointForm(EMPTY_ENDPOINT_FORM);
    } else {
      setEndpointForm({
        id: endpoint.id,
        name: endpoint.name,
        method: endpoint.method,
        url: endpoint.url,
        username: endpoint.username,
        // Left blank on purpose: the server keeps the stored secret when the
        // field is submitted empty.
        password: "",
        headers: parsePairs(endpoint.headers),
        bodyParams: parsePairs(endpoint.bodyParams),
      });
    }
    setEndpointOpen(true);
  }

  async function saveEndpoint() {
    setSaving(true);
    try {
      await api("/smstest/endpoints" + (endpointForm.id ? `/${endpointForm.id}` : ""), {
        method: endpointForm.id ? "PUT" : "POST",
        body: {
          name: endpointForm.name,
          method: endpointForm.method,
          url: endpointForm.url,
          username: endpointForm.username,
          password: endpointForm.password,
          headers: serializePairs(endpointForm.headers),
          bodyParams: serializePairs(endpointForm.bodyParams),
        },
      });
      message.success(t("已保存"));
      setEndpointOpen(false);
      await loadEndpoints();
    } catch (error) {
      message.error(apiMessage(error) || t("保存失败"));
    } finally {
      setSaving(false);
    }
  }

  async function deleteEndpoint(endpoint: SMSTestEndpoint) {
    const confirmed = await confirmDialog(
      zh
        ? `确定删除接口「${endpoint.name || endpoint.id}」吗？`
        : `Delete endpoint "${endpoint.name || endpoint.id}"?`,
      t("删除确认"),
      { type: "warning" },
    );
    if (!confirmed) return;
    try {
      await api(`/smstest/endpoints/${endpoint.id}`, { method: "DELETE" });
      message.success(t("已删除"));
      await loadEndpoints();
    } catch (error) {
      message.error(apiMessage(error) || t("删除失败"));
    }
  }

  function openScheduleEditor(schedule?: SMSTestSchedule) {
    if (!schedule) {
      setScheduleForm({ ...EMPTY_SCHEDULE_FORM, endpointId: endpoints[0]?.id ?? "" });
    } else {
      setScheduleForm({
        id: schedule.id,
        name: schedule.name,
        endpointId: schedule.endpointId,
        recipient: schedule.recipient,
        sender: schedule.sender,
        contentTemplate: schedule.contentTemplate,
        codeType: schedule.codeType,
        codeLength: schedule.codeLength,
        frequencyMinutes: schedule.frequencyMinutes,
        startTime: schedule.startTime,
        enabled: schedule.enabled,
        isExternal: schedule.isExternal,
      });
    }
    setScheduleOpen(true);
  }

  async function saveSchedule() {
    setSaving(true);
    try {
      await api("/smstest/schedules" + (scheduleForm.id ? `/${scheduleForm.id}` : ""), {
        method: scheduleForm.id ? "PUT" : "POST",
        body: {
          name: scheduleForm.name,
          endpointId: scheduleForm.endpointId,
          recipient: scheduleForm.recipient,
          sender: scheduleForm.sender,
          contentTemplate: scheduleForm.contentTemplate,
          codeType: scheduleForm.codeType,
          codeLength: scheduleForm.codeLength,
          frequencyMinutes: scheduleForm.frequencyMinutes,
          startTime: scheduleForm.startTime,
          enabled: scheduleForm.enabled,
          isExternal: scheduleForm.isExternal,
        },
      });
      message.success(t("已保存"));
      setScheduleOpen(false);
      await loadSchedules();
    } catch (error) {
      message.error(apiMessage(error) || t("保存失败"));
    } finally {
      setSaving(false);
    }
  }

  async function deleteSchedule(schedule: SMSTestSchedule) {
    const confirmed = await confirmDialog(
      zh
        ? `确定删除测试计划「${schedule.name || schedule.id}」吗？相关测试结果也会一并删除。`
        : `Delete schedule "${schedule.name || schedule.id}"? Its test results are deleted too.`,
      t("删除确认"),
      { type: "warning" },
    );
    if (!confirmed) return;
    try {
      await api(`/smstest/schedules/${schedule.id}`, { method: "DELETE" });
      message.success(t("已删除"));
      await Promise.all([loadSchedules(), loadResults()]);
    } catch (error) {
      message.error(apiMessage(error) || t("删除失败"));
    }
  }

  async function runSchedule(schedule: SMSTestSchedule) {
    try {
      await api(`/smstest/schedules/${schedule.id}/run`, { method: "POST", body: {} });
      message.success(t("测试已触发"));
      await Promise.all([loadSchedules(), loadResults()]);
    } catch (error) {
      message.error(apiMessage(error) || t("测试触发失败"));
    }
  }

  return (
    <div className="mx-auto max-w-6xl">
      <PageHeader
        title={t("短信测试")}
        subtitle={t("通过外部网关发送带校验码的短信，并统计端到端到达情况")}
      />

      <Tabs
        className="mb-6"
        value={tab}
        onChange={(key) => setTab(key as TabKey)}
        tabs={[
          { key: "statistics", label: t("统计") },
          { key: "schedules", label: t("测试计划") },
          { key: "endpoints", label: t("接口配置") },
        ]}
      />

      {tab === "statistics" && (
        <div className="space-y-4">
          <div className="flex flex-wrap items-center gap-2">
            <div className="flex gap-1">
              {[
                { value: 1, label: "1h" },
                { value: 6, label: "6h" },
                { value: 24, label: "24h" },
                { value: 168, label: "7d" },
              ].map((window) => (
                <button
                  key={window.value}
                  type="button"
                  onClick={() => setHours(window.value)}
                  className={cx(
                    "rounded-full px-3 py-1 text-xs font-medium transition-colors",
                    hours === window.value
                      ? "bg-[#0ea5e9] text-white"
                      : "bg-gray-200 text-gray-600 hover:bg-gray-300 dark:bg-white/10 dark:text-gray-300 dark:hover:bg-white/20",
                  )}
                >
                  {window.label}
                </button>
              ))}
            </div>
            <div className="ml-auto flex items-center gap-2">
              <button
                type="button"
                onClick={() => setDetailView((value) => !value)}
                className="rounded-full bg-gray-200 px-3 py-1 text-xs font-medium text-gray-600 transition-colors hover:bg-gray-300 dark:bg-white/10 dark:text-gray-300 dark:hover:bg-white/20"
              >
                {detailView ? t("图形视图") : t("表格视图")}
              </button>
              <Button size="small" variant="default" loading={loading} onClick={() => void loadResults()}>
                {t("刷新")}
              </Button>
            </div>
          </div>

          <div className="flex flex-wrap gap-x-4 gap-y-2 text-xs text-gray-500 dark:text-gray-400">
            {DOT_BANDS.map((band) => (
              <span key={band.kind} className="flex items-center gap-1.5">
                <span className={cx("inline-block h-2.5 w-2.5 shrink-0", band.className)} />
                {zh ? band.zh : band.en}
              </span>
            ))}
          </div>

          <div className="grid grid-cols-2 gap-4 sm:grid-cols-5">
            {[
              { key: "total", label: t("总计") },
              { key: "received", label: t("已送达") },
              { key: "delayed", label: t("延迟") },
              { key: "failed", label: t("失败") },
              { key: "pending", label: t("等待中") },
            ].map((tile) => (
              <div key={tile.key} className="ui-card p-4">
                <p className="text-xs text-gray-500 dark:text-gray-400">{tile.label}</p>
                <p className="mt-1 text-2xl font-bold">{summary[tile.key] ?? 0}</p>
              </div>
            ))}
          </div>

          {detailView ? (
            <div className="ui-card overflow-x-auto">
              <table className="w-full min-w-[720px] text-sm">
                <thead className="border-b border-gray-200/70 text-left text-xs uppercase text-gray-500 dark:border-white/10 dark:text-gray-400">
                  <tr>
                    <th className="px-4 py-3">{t("测试计划")}</th>
                    <th className="px-4 py-3">{t("校验码")}</th>
                    <th className="px-4 py-3">{t("状态")}</th>
                    <th className="px-4 py-3">{t("发送时间")}</th>
                    <th className="px-4 py-3">{t("接收时间")}</th>
                    <th className="px-4 py-3">{t("耗时")}</th>
                    <th className="px-4 py-3">{t("接收设备")}</th>
                  </tr>
                </thead>
                <tbody>
                  {results.length === 0 ? (
                    <tr>
                      <td className="px-4 py-8 text-center text-gray-400" colSpan={7}>
                        {t("暂无测试结果")}
                      </td>
                    </tr>
                  ) : (
                    results.map((result) => (
                      <tr key={result.id} className="border-b border-gray-100 last:border-0 dark:border-white/5">
                        <td className="px-4 py-3">{scheduleNames.get(result.scheduleId) ?? result.scheduleId}</td>
                        <td className="px-4 py-3 font-mono text-xs">{result.code}</td>
                        <td className="px-4 py-3">
                          <Tag type={statusType(result.status)}>{statusLabel(result.status, zh)}</Tag>
                        </td>
                        <td className="px-4 py-3">{formatTimestamp(result.sentAt)}</td>
                        <td className="px-4 py-3">{formatTimestamp(result.receivedAt)}</td>
                        <td className="px-4 py-3">{formatElapsed(result)}</td>
                        <td className="px-4 py-3">{result.deviceId || "--"}</td>
                      </tr>
                    ))
                  )}
                </tbody>
              </table>
            </div>
          ) : schedules.length === 0 ? (
            <div className="ui-card py-12 text-center text-sm text-gray-400">{t("暂无测试计划")}</div>
          ) : (
            <div className="space-y-4">
              {schedules.map((schedule) => {
                const scheduleResults = resultsBySchedule.get(schedule.id) ?? [];
                return (
                  <div key={schedule.id} className="ui-card p-4">
                    <div className="mb-3 flex flex-col gap-1 sm:flex-row sm:items-center sm:justify-between">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="font-medium">{schedule.name || schedule.id}</span>
                        <span className="text-xs text-gray-400">
                          {zh
                            ? `每 ${schedule.frequencyMinutes} 分钟`
                            : `every ${schedule.frequencyMinutes} min`}
                        </span>
                        {schedule.isExternal && (
                          <span className="text-xs text-[#2563eb] dark:text-[#60a5fa]">{t("外部号码")}</span>
                        )}
                        {!schedule.enabled && (
                          <span className="text-xs text-[#d9a404] dark:text-[#facc15]">{t("已停用")}</span>
                        )}
                      </div>
                      <div className="flex flex-wrap items-center gap-3 text-xs text-gray-400">
                        <span>
                          {t("接收号码")}: {schedule.recipient}
                        </span>
                        {schedule.sender && (
                          <span className="hidden sm:inline">
                            {t("发送方标识")}: {schedule.sender}
                          </span>
                        )}
                      </div>
                    </div>
                    {scheduleResults.length === 0 ? (
                      <p className="text-xs text-gray-400">{t("暂无测试结果")}</p>
                    ) : (
                      <div className="flex flex-wrap items-center gap-1">
                        {scheduleResults.map((result) => (
                          <Tooltip
                            key={result.id}
                            content={
                              <span className="block space-y-0.5 text-left">
                                <span className="block">
                                  {t("发送时间")}: {formatTimestamp(result.sentAt)}
                                </span>
                                <span className="block">
                                  {t("状态")}: {statusLabel(result.status, zh)}
                                </span>
                                <span className="block">
                                  {t("校验码")}: {result.code}
                                </span>
                                {result.receivedAt && (
                                  <span className="block">
                                    {t("接收时间")}: {formatTimestamp(result.receivedAt)} ({formatElapsed(result)})
                                  </span>
                                )}
                                {result.deviceId && (
                                  <span className="block">
                                    {t("接收设备")}: {result.deviceId}
                                  </span>
                                )}
                              </span>
                            }
                          >
                            <span
                              tabIndex={0}
                              aria-label={`${statusLabel(result.status, zh)} ${result.code}`}
                              className={cx(
                                "inline-block h-3 w-3 cursor-pointer outline-none transition-transform hover:scale-150 focus:scale-150",
                                DOT_CLASS.get(dotKind(result, schedule)),
                              )}
                            />
                          </Tooltip>
                        ))}
                      </div>
                    )}
                  </div>
                );
              })}
            </div>
          )}
        </div>
      )}

      {tab === "schedules" && (
        <div className="space-y-4">
          <div className="flex justify-end gap-2">
            <Button variant="default" loading={loading} onClick={() => void loadSchedules()}>
              {t("刷新")}
            </Button>
            <Button variant="primary" disabled={endpoints.length === 0} onClick={() => openScheduleEditor()}>
              <AddRegular className="text-[16px]" />
              {t("新建测试计划")}
            </Button>
          </div>
          {endpoints.length === 0 && (
            <p className="text-sm text-amber-600 dark:text-amber-400">
              {t("请先在「接口配置」中添加一个短信网关接口。")}
            </p>
          )}

          <div className="ui-card overflow-x-auto">
            <table className="w-full min-w-[860px] text-sm">
              <thead className="border-b border-gray-200/70 text-left text-xs uppercase text-gray-500 dark:border-white/10 dark:text-gray-400">
                <tr>
                  <th className="px-4 py-3">{t("名称")}</th>
                  <th className="px-4 py-3">{t("接口配置")}</th>
                  <th className="px-4 py-3">{t("接收号码")}</th>
                  <th className="px-4 py-3">{t("频率")}</th>
                  <th className="px-4 py-3">{t("状态")}</th>
                  <th className="px-4 py-3">{t("上次执行")}</th>
                  <th className="px-4 py-3 text-right">{t("操作")}</th>
                </tr>
              </thead>
              <tbody>
                {schedules.length === 0 ? (
                  <tr>
                    <td className="px-4 py-8 text-center text-gray-400" colSpan={7}>
                      {t("暂无测试计划")}
                    </td>
                  </tr>
                ) : (
                  schedules.map((schedule) => (
                    <tr key={schedule.id} className="border-b border-gray-100 last:border-0 dark:border-white/5">
                      <td className="px-4 py-3 font-medium">{schedule.name}</td>
                      <td className="px-4 py-3">{endpointNames.get(schedule.endpointId) ?? schedule.endpointId}</td>
                      <td className="px-4 py-3 font-mono text-xs">{schedule.recipient}</td>
                      <td className="px-4 py-3">
                        {zh
                          ? `每 ${schedule.frequencyMinutes} 分钟`
                          : `every ${schedule.frequencyMinutes} min`}
                      </td>
                      <td className="px-4 py-3">
                        <Tag type={schedule.enabled ? "success" : "info"}>
                          {schedule.enabled ? t("已启用") : t("已停用")}
                        </Tag>
                      </td>
                      <td className="px-4 py-3">{formatTimestamp(schedule.lastRunAt)}</td>
                      <td className="px-4 py-3">
                        <div className="flex justify-end gap-1">
                          <Button size="small" variant="text" onClick={() => void runSchedule(schedule)}>
                            <PlayRegular className="text-[16px]" />
                            {t("立即测试")}
                          </Button>
                          <Button size="small" variant="text" onClick={() => openScheduleEditor(schedule)}>
                            <EditRegular className="text-[16px]" />
                          </Button>
                          <Button size="small" variant="text" onClick={() => void deleteSchedule(schedule)}>
                            <DeleteRegular className="text-[16px]" />
                          </Button>
                        </div>
                      </td>
                    </tr>
                  ))
                )}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {tab === "endpoints" && (
        <div className="space-y-4">
          <div className="flex justify-end gap-2">
            <Button variant="default" loading={loading} onClick={() => void loadEndpoints()}>
              {t("刷新")}
            </Button>
            <Button variant="primary" onClick={() => openEndpointEditor()}>
              <AddRegular className="text-[16px]" />
              {t("新建接口")}
            </Button>
          </div>

          <div className="ui-card overflow-x-auto">
            <table className="w-full min-w-[720px] text-sm">
              <thead className="border-b border-gray-200/70 text-left text-xs uppercase text-gray-500 dark:border-white/10 dark:text-gray-400">
                <tr>
                  <th className="px-4 py-3">{t("名称")}</th>
                  <th className="px-4 py-3">{t("方法")}</th>
                  <th className="px-4 py-3">URL</th>
                  <th className="px-4 py-3">{t("用户名")}</th>
                  <th className="px-4 py-3 text-right">{t("操作")}</th>
                </tr>
              </thead>
              <tbody>
                {endpoints.length === 0 ? (
                  <tr>
                    <td className="px-4 py-8 text-center text-gray-400" colSpan={5}>
                      {t("暂无接口配置")}
                    </td>
                  </tr>
                ) : (
                  endpoints.map((endpoint) => (
                    <tr key={endpoint.id} className="border-b border-gray-100 last:border-0 dark:border-white/5">
                      <td className="px-4 py-3 font-medium">{endpoint.name}</td>
                      <td className="px-4 py-3">
                        <Tag type="info">{endpoint.method}</Tag>
                      </td>
                      <td className="max-w-sm truncate px-4 py-3 font-mono text-xs">{endpoint.url}</td>
                      <td className="px-4 py-3">{endpoint.username || "--"}</td>
                      <td className="px-4 py-3">
                        <div className="flex justify-end gap-1">
                          <Button size="small" variant="text" onClick={() => openEndpointEditor(endpoint)}>
                            <EditRegular className="text-[16px]" />
                          </Button>
                          <Button size="small" variant="text" onClick={() => void deleteEndpoint(endpoint)}>
                            <DeleteRegular className="text-[16px]" />
                          </Button>
                        </div>
                      </td>
                    </tr>
                  ))
                )}
              </tbody>
            </table>
          </div>
        </div>
      )}

      <Modal
        open={endpointOpen}
        onClose={() => setEndpointOpen(false)}
        width="max-w-2xl"
        title={endpointForm.id ? t("编辑接口") : t("新建接口")}
        footer={
          <div className="flex justify-end gap-2">
            <Button variant="default" onClick={() => setEndpointOpen(false)}>
              {t("取消")}
            </Button>
            <Button variant="primary" loading={saving} onClick={() => void saveEndpoint()}>
              {t("保存")}
            </Button>
          </div>
        }
      >
        <div className="space-y-4">
          <label className="block space-y-1.5 text-sm">
            <span>{t("名称")}</span>
            <Input
              value={endpointForm.name}
              onChange={(event) => setEndpointForm({ ...endpointForm, name: event.target.value })}
            />
          </label>
          <div className="grid gap-4 sm:grid-cols-[140px_1fr]">
            <label className="block space-y-1.5 text-sm">
              <span>{t("方法")}</span>
              <Select
                value={endpointForm.method}
                onChange={(value) => setEndpointForm({ ...endpointForm, method: value })}
                options={["GET", "POST", "PUT", "PATCH", "DELETE"].map((method) => ({
                  value: method,
                  label: method,
                }))}
              />
            </label>
            <label className="block space-y-1.5 text-sm">
              <span>URL</span>
              <Input
                value={endpointForm.url}
                placeholder="https://gateway.example.com/send"
                onChange={(event) => setEndpointForm({ ...endpointForm, url: event.target.value })}
              />
            </label>
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <label className="block space-y-1.5 text-sm">
              <span>{t("用户名")}</span>
              <Input
                value={endpointForm.username}
                onChange={(event) => setEndpointForm({ ...endpointForm, username: event.target.value })}
              />
            </label>
            <label className="block space-y-1.5 text-sm">
              <span>{t("密码")}</span>
              <Input
                type="password"
                value={endpointForm.password}
                placeholder={endpointForm.id ? t("留空则保持不变") : ""}
                onChange={(event) => setEndpointForm({ ...endpointForm, password: event.target.value })}
              />
            </label>
          </div>
          <PairEditor
            label={t("请求头")}
            pairs={endpointForm.headers}
            onChange={(headers) => setEndpointForm({ ...endpointForm, headers })}
          />
          <PairEditor
            label={t("请求体参数")}
            pairs={endpointForm.bodyParams}
            onChange={(bodyParams) => setEndpointForm({ ...endpointForm, bodyParams })}
          />
          <p className="text-xs text-gray-500 dark:text-gray-400">
            {t("可用占位符：{{username}}、{{password}}、{{to}}、{{from}}、{{content}}")}
          </p>
        </div>
      </Modal>

      <Modal
        open={scheduleOpen}
        onClose={() => setScheduleOpen(false)}
        width="max-w-2xl"
        title={scheduleForm.id ? t("编辑测试计划") : t("新建测试计划")}
        footer={
          <div className="flex justify-end gap-2">
            <Button variant="default" onClick={() => setScheduleOpen(false)}>
              {t("取消")}
            </Button>
            <Button variant="primary" loading={saving} onClick={() => void saveSchedule()}>
              {t("保存")}
            </Button>
          </div>
        }
      >
        <div className="space-y-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <label className="block space-y-1.5 text-sm">
              <span>{t("名称")}</span>
              <Input
                value={scheduleForm.name}
                onChange={(event) => setScheduleForm({ ...scheduleForm, name: event.target.value })}
              />
            </label>
            <label className="block space-y-1.5 text-sm">
              <span>{t("接口配置")}</span>
              <Select
                value={scheduleForm.endpointId}
                onChange={(value) => setScheduleForm({ ...scheduleForm, endpointId: value })}
                options={endpointOptions}
              />
            </label>
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <label className="block space-y-1.5 text-sm">
              <span>{t("接收号码")}</span>
              <Input
                value={scheduleForm.recipient}
                placeholder="+8613800138000"
                onChange={(event) => setScheduleForm({ ...scheduleForm, recipient: event.target.value })}
              />
            </label>
            <label className="block space-y-1.5 text-sm">
              <span>{t("发送方标识")}</span>
              <Input
                value={scheduleForm.sender}
                onChange={(event) => setScheduleForm({ ...scheduleForm, sender: event.target.value })}
              />
            </label>
          </div>
          <label className="block space-y-1.5 text-sm">
            <span>{t("短信内容模板")}</span>
            <Textarea
              rows={3}
              value={scheduleForm.contentTemplate}
              onChange={(event) =>
                setScheduleForm({ ...scheduleForm, contentTemplate: event.target.value })
              }
            />
            <span className="text-xs text-gray-500 dark:text-gray-400">
              {t("{{code}} 会被替换为随机校验码，用于匹配收到的短信。")}
            </span>
          </label>
          <div className="grid gap-4 sm:grid-cols-3">
            <label className="block space-y-1.5 text-sm">
              <span>{t("校验码类型")}</span>
              <Select
                value={scheduleForm.codeType}
                onChange={(value) => setScheduleForm({ ...scheduleForm, codeType: value })}
                options={[
                  { value: "digits", label: t("数字") },
                  { value: "letters", label: t("字母") },
                  { value: "mixed", label: t("数字+字母") },
                ]}
              />
            </label>
            <label className="block space-y-1.5 text-sm">
              <span>{t("校验码长度")}</span>
              <Input
                type="number"
                min={1}
                max={32}
                value={scheduleForm.codeLength}
                onChange={(event) =>
                  setScheduleForm({ ...scheduleForm, codeLength: Number(event.target.value) })
                }
              />
            </label>
            <label className="block space-y-1.5 text-sm">
              <span>{t("频率（分钟）")}</span>
              <Input
                type="number"
                min={1}
                value={scheduleForm.frequencyMinutes}
                onChange={(event) =>
                  setScheduleForm({ ...scheduleForm, frequencyMinutes: Number(event.target.value) })
                }
              />
            </label>
          </div>
          <label className="block space-y-1.5 text-sm">
            <span>{t("每日开始时间")}</span>
            <Input
              value={scheduleForm.startTime}
              placeholder="09:00"
              onChange={(event) => setScheduleForm({ ...scheduleForm, startTime: event.target.value })}
            />
            <span className="text-xs text-gray-500 dark:text-gray-400">
              {t("留空表示保存后立即开始。")}
            </span>
          </label>
          <div className="flex items-center justify-between">
            <span className="text-sm">{t("启用")}</span>
            <Switch
              checked={scheduleForm.enabled}
              onChange={(enabled) => setScheduleForm({ ...scheduleForm, enabled })}
            />
          </div>
          <div className="flex items-center justify-between">
            <div>
              <p className="text-sm">{t("外部号码")}</p>
              <p className="text-xs text-gray-500 dark:text-gray-400">
                {t("接收号码不在本机模组上，只记录发送结果，不等待回收。")}
              </p>
            </div>
            <Switch
              checked={scheduleForm.isExternal}
              onChange={(isExternal) => setScheduleForm({ ...scheduleForm, isExternal })}
            />
          </div>
        </div>
      </Modal>

      <div className="mt-6 flex items-center gap-2 text-xs text-gray-400 dark:text-gray-500">
        <SendClockRegular className="text-[16px]" />
        <span>{t("调度器每 10 秒检查一次到期的测试计划与待匹配的短信。")}</span>
      </div>
    </div>
  );
}
