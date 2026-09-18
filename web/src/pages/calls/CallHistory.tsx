import { useCallback, useEffect, useState } from "react";
import { ArrowClockwiseRegular, CallInboundRegular, CallOutboundRegular } from "@fluentui/react-icons";
import { apiMessage, listCallRecords } from "../../api";
import type { CallRecord } from "../../types";
import { Button, Input, Select, StatusDot, Tag } from "../../components/ui";
import type { StatusTone } from "../../components/ui";
import { useI18n } from "../../lib/i18n";

const PAGE = 25;

// Answered is the only unambiguously good outcome; busy and no-answer are
// ordinary; failed is the one worth noticing in a long list.
function dispositionTone(disposition: string | undefined): StatusTone {
  switch (disposition) {
    case "answered":
      return "success";
    case "failed":
      return "danger";
    case "":
    case undefined:
      return "neutral";
    default:
      return "warning";
  }
}

// Duration counts from the answer, so a call that only rang shows a dash
// rather than 0:00 -- there is nothing to have lasted.
function duration(seconds: number) {
  if (!seconds) return "—";
  const minutes = Math.floor(seconds / 60);
  return `${minutes}:${String(seconds % 60).padStart(2, "0")}`;
}

export function CallHistory({ deviceId }: { deviceId: string }) {
  const { t } = useI18n();
  const [records, setRecords] = useState<CallRecord[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [search, setSearch] = useState("");
  const [disposition, setDisposition] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  const load = useCallback(() => {
    setLoading(true);
    listCallRecords({ deviceId, search, disposition, limit: PAGE, offset })
      .then((response) => {
        setRecords(response.records ?? []);
        setTotal(response.total ?? 0);
        setError("");
      })
      .catch((problem) => setError(apiMessage(problem)))
      .finally(() => setLoading(false));
  }, [deviceId, search, disposition, offset]);

  useEffect(() => {
    load();
  }, [load]);

  // A filter change can leave you on a page that no longer exists under it,
  // so start again from the top.
  useEffect(() => {
    setOffset(0);
  }, [deviceId, search, disposition]);

  return (
    <div className="ui-card mt-4 p-4">
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <h2 className="text-sm font-medium text-slate-600 dark:text-slate-300">{t("通话记录")}</h2>
        <span className="text-xs text-slate-400">{total}</span>
        <div className="ml-auto flex flex-wrap items-center gap-2">
          <div className="w-40">
            <Input
              value={search}
              placeholder={t("按号码搜索")}
              onChange={(event) => setSearch(event.target.value)}
            />
          </div>
          <div className="w-32">
            <Select
              value={disposition}
              onChange={setDisposition}
              options={[
                { value: "", label: t("全部") },
                { value: "answered", label: t("已接通") },
                { value: "no_answer", label: t("无人接听") },
                { value: "busy", label: t("占线") },
                { value: "cancelled", label: t("已取消") },
                { value: "failed", label: t("失败") },
              ]}
            />
          </div>
          <Button icon={<ArrowClockwiseRegular />} loading={loading} onClick={load}>
            {t("刷新")}
          </Button>
        </div>
      </div>

      {error ? <p className="mb-2 text-xs text-red-500">{error}</p> : null}

      <div className="divide-y divide-slate-100 dark:divide-slate-800">
        {records.length === 0 ? (
          <p className="py-3 text-sm text-slate-500 dark:text-slate-400">
            {t("还没有通话记录。通话结束后会自动出现在这里。")}
          </p>
        ) : null}
        {records.map((record) => (
          <div key={record.id} className="flex flex-wrap items-center gap-2 py-2 text-sm">
            <StatusDot tone={dispositionTone(record.disposition)} />
            {record.direction === "incoming" ? (
              <CallInboundRegular className="text-slate-400" />
            ) : (
              <CallOutboundRegular className="text-slate-400" />
            )}
            <span className="font-mono">{record.peerNumber || "—"}</span>
            {record.disposition ? (
              <Tag type={record.disposition === "answered" ? "success" : "info"}>
                {t(record.disposition)}
              </Tag>
            ) : (
              <Tag type="warning">{t("进行中")}</Tag>
            )}
            {/* Which side drove it: VoCat's own page, or a phone through the PBX. */}
            {record.source ? <Tag type="info">{record.source}</Tag> : null}
            <span className="ml-auto font-mono text-xs text-slate-400">
              {duration(record.durationSeconds)}
            </span>
            <span className="w-44 text-right font-mono text-xs text-slate-400">
              {new Date(record.startedAt).toLocaleString()}
            </span>
          </div>
        ))}
      </div>

      {total > PAGE ? (
        <div className="mt-3 flex items-center justify-end gap-2">
          <Button
            size="small"
            disabled={offset === 0}
            onClick={() => setOffset((current) => Math.max(0, current - PAGE))}
          >
            {t("上一页")}
          </Button>
          <span className="text-xs text-slate-400">
            {offset + 1}–{Math.min(offset + PAGE, total)}
          </span>
          <Button
            size="small"
            disabled={offset + PAGE >= total}
            onClick={() => setOffset((current) => current + PAGE)}
          >
            {t("下一页")}
          </Button>
        </div>
      ) : null}
    </div>
  );
}
