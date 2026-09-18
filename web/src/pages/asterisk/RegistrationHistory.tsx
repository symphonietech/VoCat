import { useCallback, useEffect, useState } from "react";
import { ArrowClockwiseRegular } from "@fluentui/react-icons";
import { apiMessage, getAsteriskRegistrations } from "../../api";
import type { AsteriskRegistration } from "../../types";
import { Button, StatusDot, Tag } from "../../components/ui";
import type { StatusTone } from "../../components/ui";
import { useI18n } from "../../lib/i18n";

function statusTone(status: string | undefined): StatusTone {
  if (/^reachable$/i.test(status ?? "")) return "success";
  if (/^(unreachable|removed|unregistered)$/i.test(status ?? "")) return "danger";
  return "warning";
}

// Short URIs: Linphone packs its push token into the contact, which buries
// the part anyone reads.
function shortContact(uri: string | undefined) {
  if (!uri) return "";
  const [base] = uri.split(";");
  return base;
}

export function RegistrationHistory() {
  const { t } = useI18n();
  const [events, setEvents] = useState<AsteriskRegistration[]>([]);
  const [recording, setRecording] = useState(true);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [open, setOpen] = useState(false);

  const load = useCallback(() => {
    setLoading(true);
    getAsteriskRegistrations(50)
      .then((response) => {
        setEvents(response.registrations ?? []);
        setRecording(Boolean(response.recording));
        setError("");
      })
      .catch((problem) => setError(apiMessage(problem)))
      .finally(() => setLoading(false));
  }, []);

  // Loaded on demand rather than with the page's poll: this is history, so a
  // refresh every five seconds would be five requests for the same rows.
  useEffect(() => {
    if (open) load();
  }, [open, load]);

  return (
    <div className="mb-4">
      <div className="mb-2 flex flex-wrap items-center gap-2">
        <h2 className="text-sm font-medium text-slate-600 dark:text-slate-300">{t("注册变更记录")}</h2>
        {!recording ? <Tag type="warning">{t("未记录")}</Tag> : null}
        <div className="ml-auto flex gap-2">
          {open ? (
            <Button icon={<ArrowClockwiseRegular />} loading={loading} onClick={load}>
              {t("刷新")}
            </Button>
          ) : null}
          <Button onClick={() => setOpen((value) => !value)}>
            {open ? t("收起") : t("展开")}
          </Button>
        </div>
      </div>

      {open ? (
        <div className="ui-card divide-y divide-slate-100 dark:divide-slate-800">
          {error ? <p className="p-3 text-xs text-red-500">{error}</p> : null}
          {!error && events.length === 0 ? (
            <p className="p-3 text-sm text-slate-500 dark:text-slate-400">
              {recording
                ? t("还没有注册变更。只有状态改变时才会记录，稳定的分机不会产生记录。")
                : t("未配置管理接口，因此不会记录注册变更。")}
            </p>
          ) : null}
          {events.map((event, index) => (
            <div key={index} className="flex flex-wrap items-center gap-2 p-2 text-xs">
              <StatusDot tone={statusTone(event.status)} />
              <span className="font-medium">{event.endpoint}</span>
              {event.previousStatus ? (
                <span className="text-slate-400">
                  {event.previousStatus} →
                </span>
              ) : null}
              <Tag type={statusTone(event.status) === "success" ? "success" : "info"}>
                {event.status}
              </Tag>
              <span className="break-all text-slate-500 dark:text-slate-400" title={event.contactUri}>
                {shortContact(event.contactUri)}
              </span>
              {event.userAgent ? <span className="truncate text-slate-400">{event.userAgent}</span> : null}
              <span className="ml-auto font-mono text-slate-400">
                {new Date(event.changedAt).toLocaleString()}
              </span>
            </div>
          ))}
        </div>
      ) : null}
    </div>
  );
}
