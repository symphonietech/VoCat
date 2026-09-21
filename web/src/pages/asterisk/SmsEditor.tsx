import { useCallback, useEffect, useMemo, useState } from "react";
import { ArrowSyncRegular } from "@fluentui/react-icons";
import {
  apiMessage,
  applyAsteriskExtensions,
  getAsteriskExtensions,
  getAsteriskSMSHistory,
  saveAsteriskSMS,
} from "../../api";
import type { AsteriskSMS, AsteriskSMSMessage } from "../../types";
import { Button, EmptyState, RefreshButton, Select, Tag, message } from "../../components/ui";
import { useI18n } from "../../lib/i18n";

const DEFAULT: AsteriskSMS = { mode: "off" };

// One conversation: an extension and the outside number it exchanged texts
// with. Grouped by both because an extension that texts three people has
// three threads, exactly as a handset shows them.
type Thread = {
  key: string;
  extension: string;
  peer: string;
  messages: AsteriskSMSMessage[];
  latest: string;
};

function threadKey(extension: string, peer: string) {
  return `${extension}\u0000${peer}`;
}

// Newest first, because the question a person opens this tab with is almost
// always about the message that just arrived.
function buildThreads(messages: AsteriskSMSMessage[]): Thread[] {
  const threads = new Map<string, Thread>();
  for (const entry of messages) {
    const extension = entry.extension ?? "";
    if (!extension) continue;
    const key = threadKey(extension, entry.peer);
    const existing = threads.get(key);
    if (existing) {
      existing.messages.push(entry);
      if (entry.timestamp > existing.latest) existing.latest = entry.timestamp;
      continue;
    }
    threads.set(key, {
      key,
      extension,
      peer: entry.peer,
      messages: [entry],
      latest: entry.timestamp,
    });
  }
  return Array.from(threads.values()).sort((left, right) =>
    right.latest.localeCompare(left.latest),
  );
}

function formatTime(value: string) {
  const at = new Date(value);
  return Number.isNaN(at.getTime()) ? value : at.toLocaleString();
}

export function SmsEditor() {
  const { t } = useI18n();
  const [sms, setSms] = useState<AsteriskSMS>(DEFAULT);
  const [trunkHost, setTrunkHost] = useState("");
  const [preview, setPreview] = useState("");
  const [canApply, setCanApply] = useState(false);
  const [dirty, setDirty] = useState(false);
  const [busy, setBusy] = useState(false);
  const [history, setHistory] = useState<AsteriskSMSMessage[]>([]);
  const [loading, setLoading] = useState(true);
  const [extension, setExtension] = useState("");
  const [thread, setThread] = useState("");

  const loadSettings = useCallback(() => {
    getAsteriskExtensions()
      .then((next) => {
        setSms(next.sms ?? DEFAULT);
        setTrunkHost(next.trunkHost ?? "");
        setPreview(next.smsPreview ?? "");
        setCanApply(Boolean(next.canApply));
      })
      .catch((error) => message.error(apiMessage(error)));
  }, []);

  const loadHistory = useCallback(() => {
    setLoading(true);
    getAsteriskSMSHistory()
      .then((next) => setHistory(next.messages ?? []))
      // An empty history and an unreadable one look the same on screen, so
      // the failure is said out loud rather than shown as "no messages yet".
      .catch((error) => message.error(apiMessage(error)))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    loadSettings();
    loadHistory();
  }, [loadSettings, loadHistory]);

  const threads = useMemo(() => buildThreads(history), [history]);
  const extensions = useMemo(() => {
    const seen = new Map<string, string>();
    for (const entry of threads) {
      if (!seen.has(entry.extension)) seen.set(entry.extension, entry.latest);
    }
    return Array.from(seen.keys());
  }, [threads]);

  // Selection follows the data rather than being pinned to it: a reload that
  // drops the selected extension would otherwise leave both panes empty with
  // no way back except reloading the page.
  const selectedExtension = extension && extensions.includes(extension) ? extension : extensions[0] ?? "";
  const extensionThreads = useMemo(
    () => threads.filter((entry) => entry.extension === selectedExtension),
    [threads, selectedExtension],
  );
  const selectedThread =
    extensionThreads.find((entry) => entry.key === thread) ?? extensionThreads[0];

  const save = async () => {
    setBusy(true);
    try {
      const result = await saveAsteriskSMS(sms);
      setPreview(result.smsPreview ?? "");
      setTrunkHost(result.trunkHost ?? "");
      setDirty(false);
      message.success(t("已保存"));
      loadSettings();
    } catch (error) {
      // The server refuses a mode it cannot render a working dialplan for,
      // and says which piece is missing.
      message.error(apiMessage(error));
    } finally {
      setBusy(false);
    }
  };

  const apply = async () => {
    setBusy(true);
    try {
      await applyAsteriskExtensions();
      message.success(t("已应用"));
      loadSettings();
    } catch (error) {
      message.error(apiMessage(error));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-4">
      <div>
        <div className="mb-2 flex flex-wrap items-center gap-2">
          <h2 className="text-sm font-medium text-slate-600 dark:text-slate-300">
            {t("短信路由")}
          </h2>
          {dirty ? <Tag type="warning">{t("未保存")}</Tag> : null}
          <div className="ml-auto flex flex-wrap gap-2">
            <Button variant="primary" loading={busy} disabled={!dirty} onClick={() => void save()}>
              {t("保存")}
            </Button>
            <Button
              icon={<ArrowSyncRegular />}
              loading={busy}
              disabled={dirty || !canApply}
              onClick={() => void apply()}
            >
              {t("应用")}
            </Button>
          </div>
        </div>

        <div className="ui-card space-y-3 p-3">
          <Select
            value={sms.mode}
            onChange={(value) => {
              setSms({ mode: value as AsteriskSMS["mode"] });
              setDirty(true);
            }}
            options={[
              { value: "off", label: t("不经过 PBX") },
              { value: "did", label: t("按号码收发（分机号即 SIM 号码）") },
            ]}
          />
          {sms.mode === "did" ? (
            <p className="text-xs text-slate-500 dark:text-slate-400">
              {t(
                "SIM 收到的短信会送到与该 SIM 号码同名的分机，原始发件人保留在 From 中，分机可直接回复。分机发出的短信只有在分机号等于某张 SIM 的号码时才被接受，并由该 SIM 发出；收件人可以是任意号码。",
              )}
            </p>
          ) : (
            <p className="text-xs text-slate-500 dark:text-slate-400">
              {t("短信只在 VoCat 内处理，不生成任何 Asterisk 短信拨号方案。")}
            </p>
          )}
          {!trunkHost ? (
            <p className="rounded-lg bg-amber-50 p-2 text-xs text-amber-700 dark:bg-amber-500/10 dark:text-amber-300">
              {t("还没有配置 SIP 中继，短信无法经过 PBX。请先设置 VOCAT_SIP_TRUNK_ADDR 并重启。")}
            </p>
          ) : null}
        </div>

        {preview ? (
          <details className="mt-2">
            <summary className="cursor-pointer text-xs text-sky-600 dark:text-sky-400">
              {t("生成的短信拨号方案")}
            </summary>
            <pre className="mt-2 overflow-x-auto rounded-lg bg-slate-50 p-3 text-xs dark:bg-slate-800/50">
              {preview}
            </pre>
          </details>
        ) : null}
      </div>

      <div>
        <div className="mb-2 flex flex-wrap items-center gap-2">
          <h2 className="text-sm font-medium text-slate-600 dark:text-slate-300">
            {t("短信记录")}
          </h2>
          <span className="text-xs text-slate-400 dark:text-slate-500">
            {t("仅显示经过 PBX 的短信")}
          </span>
          <div className="ml-auto">
            <RefreshButton loading={loading} onClick={loadHistory} />
          </div>
        </div>

        {threads.length === 0 ? (
          <div className="ui-card p-4">
            <EmptyState
              title={t("还没有经过 PBX 的短信")}
              subtitle={t("分机收发的短信会出现在这里。")}
            />
          </div>
        ) : (
          <div className="ui-card grid h-[26rem] grid-cols-1 divide-y divide-slate-100 overflow-hidden sm:grid-cols-[10rem_14rem_1fr] sm:divide-x sm:divide-y-0 dark:divide-slate-800">
            <div className="overflow-y-auto p-2">
              <div className="px-2 pb-2 text-xs font-medium uppercase tracking-wider text-slate-400">
                {t("分机")}
              </div>
              {extensions.map((name) => (
                <button
                  key={name}
                  type="button"
                  onClick={() => {
                    setExtension(name);
                    setThread("");
                  }}
                  className={`mb-1 block w-full truncate rounded-lg px-2 py-1.5 text-left font-mono text-xs ${
                    name === selectedExtension
                      ? "bg-sky-50 text-sky-700 dark:bg-sky-500/10 dark:text-sky-300"
                      : "text-slate-600 hover:bg-slate-50 dark:text-slate-300 dark:hover:bg-slate-800/50"
                  }`}
                >
                  {name}
                </button>
              ))}
            </div>

            <div className="overflow-y-auto p-2">
              <div className="px-2 pb-2 text-xs font-medium uppercase tracking-wider text-slate-400">
                {t("会话")}
              </div>
              {extensionThreads.map((entry) => (
                <button
                  key={entry.key}
                  type="button"
                  onClick={() => setThread(entry.key)}
                  className={`mb-1 block w-full rounded-lg px-2 py-1.5 text-left ${
                    entry.key === selectedThread?.key
                      ? "bg-sky-50 dark:bg-sky-500/10"
                      : "hover:bg-slate-50 dark:hover:bg-slate-800/50"
                  }`}
                >
                  <div className="truncate font-mono text-xs text-slate-700 dark:text-slate-200">
                    {entry.peer}
                  </div>
                  <div className="truncate text-[11px] text-slate-400">
                    {formatTime(entry.latest)}
                  </div>
                </button>
              ))}
            </div>

            <div className="overflow-y-auto p-3">
              {selectedThread ? (
                selectedThread.messages
                  .slice()
                  .sort((left, right) => left.timestamp.localeCompare(right.timestamp))
                  .map((entry) => {
                    // Which side of the bubble: a message the extension sent
                    // is the extension's own, everything else came to it.
                    const fromExtension = entry.trunkRole === "sent_by_extension";
                    return (
                      <div
                        key={entry.id}
                        className={`mb-2 flex ${fromExtension ? "justify-end" : "justify-start"}`}
                      >
                        <div
                          className={`max-w-[85%] rounded-xl px-3 py-2 text-xs ${
                            fromExtension
                              ? "bg-sky-500 text-white"
                              : "bg-slate-100 text-slate-700 dark:bg-slate-800 dark:text-slate-200"
                          }`}
                        >
                          <div className="whitespace-pre-wrap break-words">{entry.body}</div>
                          <div
                            className={`mt-1 flex flex-wrap gap-2 text-[11px] ${
                              fromExtension ? "text-sky-100" : "text-slate-400"
                            }`}
                          >
                            <span>{formatTime(entry.timestamp)}</span>
                            {entry.deviceId ? <span>{entry.deviceId}</span> : null}
                            {/* The delivery state is the whole reason an
                                outbound row is worth opening: it is what the
                                extension was told in the report text. */}
                            {fromExtension && entry.deliveryState ? (
                              <span>{entry.deliveryState}</span>
                            ) : null}
                          </div>
                        </div>
                      </div>
                    );
                  })
              ) : (
                <p className="text-xs text-slate-400">{t("选择一个会话")}</p>
              )}
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
