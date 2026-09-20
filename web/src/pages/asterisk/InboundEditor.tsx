import { useCallback, useEffect, useState } from "react";
import { ArrowSyncRegular } from "@fluentui/react-icons";
import {
  apiMessage,
  getAsteriskExtensions,
  getAsteriskTrunks,
  saveAsteriskInbound,
  applyAsteriskExtensions,
} from "../../api";
import type { AsteriskExtension, AsteriskInbound, AsteriskTrunk } from "../../types";
import { Button, Input, Select, Tag, message } from "../../components/ui";
import { useI18n } from "../../lib/i18n";

const DEFAULT: AsteriskInbound = { mode: "ring_all", extensions: [], ringSeconds: 30, huntSeconds: 15 };

// Where a call arriving on a SIM goes. The four modes are exclusive: there is
// no fallback between them, because a fallback is exactly the thing that makes
// "why did that call ring the wrong phone" unanswerable.
export function InboundEditor() {
  const { t } = useI18n();
  const [inbound, setInbound] = useState<AsteriskInbound>(DEFAULT);
  const [available, setAvailable] = useState<AsteriskExtension[]>([]);
  const [trunks, setTrunks] = useState<AsteriskTrunk[]>([]);
  const [preview, setPreview] = useState("");
  const [canApply, setCanApply] = useState(false);
  const [dirty, setDirty] = useState(false);
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    getAsteriskExtensions()
      .then((next) => {
        setInbound(next.inbound ?? DEFAULT);
        setAvailable(next.extensions ?? []);
        setPreview(next.inboundPreview ?? "");
        setCanApply(Boolean(next.canApply));
      })
      .catch((error) => message.error(apiMessage(error)));
    // Separately, because forward mode picks from the trunk list rather than
    // the extension list. A failure here only empties the picker, so it is
    // not worth failing the whole page for.
    getAsteriskTrunks()
      .then((next) => setTrunks(next.trunks ?? []))
      .catch(() => setTrunks([]));
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const update = (patch: Partial<AsteriskInbound>) => {
    setInbound((current) => ({ ...current, ...patch }));
    setDirty(true);
  };

  const save = async () => {
    setBusy(true);
    try {
      const result = await saveAsteriskInbound(inbound);
      setPreview(result.inboundPreview ?? "");
      setDirty(false);
      message.success(t("已保存"));
      load();
    } catch (error) {
      // The server refuses a ring group naming an account that does not
      // exist, and names the offending extension.
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
      load();
    } catch (error) {
      message.error(apiMessage(error));
    } finally {
      setBusy(false);
    }
  };

  const names = available.map((extension) => extension.name).filter(Boolean);
  const group = inbound.extensions ?? [];

  return (
    <div className="mb-4">
      <div className="mb-2 flex flex-wrap items-center gap-2">
        <h2 className="text-sm font-medium text-slate-600 dark:text-slate-300">{t("来电路由")}</h2>
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
        <div className="grid gap-2 sm:grid-cols-[1fr_8rem]">
          <Select
            value={inbound.mode}
            onChange={(value) => update({ mode: value as AsteriskInbound["mode"] })}
            options={[
              { value: "did", label: t("拨给同号码分机") },
              { value: "ring_all", label: t("全部同时振铃") },
              { value: "hunt", label: t("按顺序轮询") },
              { value: "forward", label: t("转发到外部中继") },
            ]}
          />
          <Input
            value={String(inbound.mode === "hunt" ? inbound.huntSeconds : inbound.ringSeconds)}
            title={t("振铃秒数")}
            onChange={(event) => {
              const seconds = Number(event.target.value.replace(/\D/g, "")) || 0;
              update(inbound.mode === "hunt" ? { huntSeconds: seconds } : { ringSeconds: seconds });
            }}
          />
        </div>

        {inbound.mode === "forward" ? (
          <div className="space-y-2">
            <p className="text-xs text-slate-500 dark:text-slate-400">
              {t("呼入的通话直接转发到所选中继，本地分机不振铃。目标号码留空则把被叫号码原样传给对端。")}
            </p>
            <div className="grid gap-2 sm:grid-cols-2">
              <Select
                value={inbound.forwardTrunk ?? ""}
                placeholder={t("选择中继")}
                onChange={(value) => update({ forwardTrunk: value })}
                options={trunks.map((trunk) => ({ value: trunk.name, label: trunk.name }))}
              />
              <Input
                value={inbound.forwardNumber ?? ""}
                placeholder={t("目标号码（留空则透传被叫号码）")}
                onChange={(event) => update({ forwardNumber: event.target.value })}
              />
            </div>
            {trunks.length === 0 ? (
              <p className="rounded-lg bg-amber-50 p-2 text-xs text-amber-700 dark:bg-amber-500/10 dark:text-amber-300">
                {t("还没有配置外部中继。先在上面的「外部中继」中添加一个，保存并应用后才能在这里选择。")}
              </p>
            ) : null}
          </div>
        ) : null}

        {inbound.mode === "did" ? (
          <p className="text-xs text-slate-500 dark:text-slate-400">
            {t("按拨入号码振铃与之同名的分机。给每张 SIM 建一个以其号码命名的分机即可，无需额外映射；没有对应分机的号码会被拒接。")}
          </p>
        ) : null}

        {inbound.mode !== "did" && inbound.mode !== "forward" ? (
          <div>
            <p className="mb-1 text-xs text-slate-500 dark:text-slate-400">
              {inbound.mode === "hunt"
                ? t("按此顺序依次振铃，前一个无人接听才轮到下一个。")
                : t("留空表示所有分机，新增分机会自动加入。")}
            </p>
            <div className="flex flex-wrap gap-1">
              {names.map((name) => {
                const index = group.indexOf(name);
                const picked = index >= 0;
                return (
                  <button
                    key={name}
                    type="button"
                    className={`rounded-lg border px-2 py-1 font-mono text-xs ${
                      picked
                        ? "border-sky-400 bg-sky-50 text-sky-700 dark:bg-sky-500/10 dark:text-sky-300"
                        : "border-slate-200 text-slate-500 dark:border-slate-700 dark:text-slate-400"
                    }`}
                    onClick={() =>
                      update({
                        // Appending rather than sorting: in hunt mode the
                        // order is the decision, so clicks build it.
                        extensions: picked
                          ? group.filter((entry) => entry !== name)
                          : [...group, name],
                      })
                    }
                  >
                    {picked && inbound.mode === "hunt" ? `${index + 1}. ` : ""}
                    {name}
                  </button>
                );
              })}
              {names.length === 0 ? (
                <span className="text-xs text-slate-400">{t("还没有分机")}</span>
              ) : null}
            </div>
          </div>
        ) : null}
      </div>

      {preview ? (
        <details className="mt-2">
          <summary className="cursor-pointer text-xs text-sky-600 dark:text-sky-400">
            {t("生成的来电拨号方案")}
          </summary>
          <pre className="mt-2 overflow-x-auto rounded-lg bg-slate-50 p-3 text-xs dark:bg-slate-800/50">
            {preview}
          </pre>
        </details>
      ) : null}
    </div>
  );
}
