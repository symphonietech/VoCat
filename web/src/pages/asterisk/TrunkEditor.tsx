import { useCallback, useEffect, useState } from "react";
import { AddRegular, DeleteRegular, ArrowSyncRegular } from "@fluentui/react-icons";
import { apiMessage, getAsteriskTrunks, saveAsteriskTrunks, applyAsteriskTrunks } from "../../api";
import type { AsteriskTrunk, AsteriskTrunks } from "../../types";
import { Button, Input, Select, Switch, Tag, message } from "../../components/ui";
import { useI18n } from "../../lib/i18n";

const DEFAULT_TIMEOUT = 60;
const DEFAULT_PORT = 5060;
// Deliberately small. A trunk cannot carry more concurrent calls than there
// are idle SIMs anyway, and a low cap is the difference between a bad hour
// and a bad month if the peer is abused.
const DEFAULT_CONCURRENT = 4;

// A new trunk has no destinations, so it reaches nothing until one is added.
// That is the intended starting state, not an oversight.
function blankTrunk(): AsteriskTrunk {
  return {
    name: "",
    host: "",
    port: DEFAULT_PORT,
    transport: "udp",
    match: [],
    outboundUsername: "",
    outboundPassword: "",
    destinations: [],
    devices: [],
    maxConcurrent: DEFAULT_CONCURRENT,
    timeoutSeconds: DEFAULT_TIMEOUT,
    shareRoutes: false,
    comment: "",
  };
}

function splitList(value: string): string[] {
  return value.split(/[\s,]+/).filter(Boolean);
}

export function TrunkEditor() {
  const { t } = useI18n();
  const [state, setState] = useState<AsteriskTrunks | null>(null);
  const [trunks, setTrunks] = useState<AsteriskTrunk[]>([]);
  const [dirty, setDirty] = useState(false);
  const [busy, setBusy] = useState(false);

  const load = useCallback((keepEdits: boolean) => {
    getAsteriskTrunks()
      .then((next) => {
        setState(next);
        // Server state replaces local edits only when there are none to lose.
        if (!keepEdits) setTrunks(next.trunks ?? []);
      })
      .catch((error) => message.error(apiMessage(error)));
  }, []);

  useEffect(() => {
    load(false);
  }, [load]);

  const update = (index: number, patch: Partial<AsteriskTrunk>) => {
    setTrunks((current) =>
      current.map((trunk, at) => (at === index ? { ...trunk, ...patch } : trunk)),
    );
    setDirty(true);
  };

  const save = async () => {
    setBusy(true);
    try {
      await saveAsteriskTrunks(trunks);
      setDirty(false);
      message.success(t("已保存"));
      load(false);
    } catch (error) {
      // The server refuses a trunk rather than storing one that could never
      // be applied, and names the offending peer.
      message.error(apiMessage(error));
    } finally {
      setBusy(false);
    }
  };

  const apply = async () => {
    setBusy(true);
    try {
      await applyAsteriskTrunks();
      message.success(t("已应用"));
      load(false);
    } catch (error) {
      message.error(apiMessage(error));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="mb-4">
      <div className="mb-2 flex flex-wrap items-center gap-2">
        <h2 className="text-sm font-medium text-slate-600 dark:text-slate-300">{t("外部中继")}</h2>
        {dirty ? <Tag type="warning">{t("未保存")}</Tag> : null}
        {!dirty && state?.pending ? <Tag type="warning">{t("待应用")}</Tag> : null}
        <div className="ml-auto flex flex-wrap gap-2">
          <Button
            icon={<AddRegular />}
            onClick={() => {
              setTrunks((current) => [...current, blankTrunk()]);
              setDirty(true);
            }}
          >
            {t("添加外部中继")}
          </Button>
          <Button variant="primary" loading={busy} disabled={!dirty} onClick={save}>
            {t("保存")}
          </Button>
          <Button
            icon={<ArrowSyncRegular />}
            loading={busy}
            // Applying an unsaved edit would reload the previous peers and
            // report success, which is worse than refusing to run.
            disabled={dirty || !state?.canApply}
            onClick={apply}
          >
            {t("应用")}
          </Button>
        </div>
      </div>

      <p className="mb-2 rounded-lg bg-amber-50 p-2 text-xs text-amber-700 dark:bg-amber-500/10 dark:text-amber-300">
        {t("外部中继可以通过 SIM 卡拨出，产生真实话费。只填写确实需要的目标号码和 SIM 卡。")}
      </p>

      {state?.unknownDevices?.length ? (
        <p className="mb-2 rounded-lg bg-amber-50 p-2 text-xs text-amber-700 dark:bg-amber-500/10 dark:text-amber-300">
          {t("这些设备已不存在，使用它们的外部中继在拨号时会失败")}: {state.unknownDevices.join(", ")}
        </p>
      ) : null}

      {state && !state.canApply ? (
        <p className="mb-2 rounded-lg bg-amber-50 p-2 text-xs text-amber-700 dark:bg-amber-500/10 dark:text-amber-300">
          {t("未配置管理接口，保存后需手动重启 Asterisk 容器才能生效")}
        </p>
      ) : null}

      <div className="ui-card divide-y divide-slate-100 dark:divide-slate-800">
        {trunks.length === 0 ? (
          <p className="p-3 text-sm text-slate-500 dark:text-slate-400">
            {t("没有外部中继。外部 SIP 对端无法接入，也无法作为转发目标。")}
          </p>
        ) : null}
        {trunks.map((trunk, index) => (
          <div key={index} className="space-y-2 p-3">
            <div className="grid gap-2 sm:grid-cols-[1fr_1.5fr_5rem_6rem_auto]">
              <Input
                value={trunk.name}
                placeholder={t("名称")}
                onChange={(event) => update(index, { name: event.target.value })}
              />
              <Input
                value={trunk.host}
                placeholder={t("对端地址")}
                onChange={(event) => update(index, { host: event.target.value })}
              />
              <Input
                value={String(trunk.port)}
                onChange={(event) =>
                  update(index, { port: Number(event.target.value.replace(/\D/g, "")) || 0 })
                }
              />
              <Select
                value={trunk.transport}
                onChange={(value) => update(index, { transport: value })}
                options={[
                  { value: "udp", label: "UDP" },
                  { value: "tcp", label: "TCP" },
                ]}
              />
              <Button
                icon={<DeleteRegular />}
                onClick={() => {
                  setTrunks((current) => current.filter((_, at) => at !== index));
                  setDirty(true);
                }}
              />
            </div>

            <Input
              value={(trunk.match ?? []).join(" ")}
              placeholder={t("允许的来源地址或网段")}
              onChange={(event) => update(index, { match: splitList(event.target.value) })}
            />

            {/* Two credential pairs, deliberately separate. The inbound one
                authenticates what the peer sends; the outbound one answers a
                challenge to what this side sends, which is what a provider
                does when a call is forwarded out to it. A provider using one
                credential for both directions gets it entered twice. */}
            <div className="grid gap-2 sm:grid-cols-2">
              <Input
                value={trunk.username ?? ""}
                placeholder={t("呼入用户名（可选）")}
                onChange={(event) => update(index, { username: event.target.value })}
              />
              <Input
                type="password"
                value={trunk.password ?? ""}
                placeholder={
                  trunk.hasPassword ? t("已设置，留空则不修改") : t("呼入密码（保存后不再回显）")
                }
                onChange={(event) => update(index, { password: event.target.value })}
              />
              <Input
                value={trunk.outboundUsername ?? ""}
                placeholder={t("呼出用户名（可选）")}
                onChange={(event) => update(index, { outboundUsername: event.target.value })}
              />
              <Input
                type="password"
                value={trunk.outboundPassword ?? ""}
                placeholder={
                  trunk.hasOutboundPassword
                    ? t("已设置，留空则不修改")
                    : t("呼出密码（保存后不再回显）")
                }
                onChange={(event) => update(index, { outboundPassword: event.target.value })}
              />
            </div>

            <div className="grid gap-2 sm:grid-cols-[2fr_2fr_5rem_5rem]">
              <Input
                value={(trunk.destinations ?? []).join(" ")}
                placeholder={t("允许拨打的号码模式，如 _1NXXNXXXXXX")}
                onChange={(event) => update(index, { destinations: splitList(event.target.value) })}
              />
              <Input
                value={trunk.devices.join(" ")}
                placeholder={t("允许使用的 SIM，用空格分隔")}
                onChange={(event) => update(index, { devices: splitList(event.target.value) })}
              />
              <Input
                value={String(trunk.maxConcurrent)}
                onChange={(event) =>
                  update(index, {
                    maxConcurrent: Number(event.target.value.replace(/\D/g, "")) || 0,
                  })
                }
              />
              <Input
                value={String(trunk.timeoutSeconds)}
                onChange={(event) =>
                  update(index, {
                    timeoutSeconds: Number(event.target.value.replace(/\D/g, "")) || 0,
                  })
                }
              />
            </div>

            <div className="flex flex-wrap items-center gap-2">
              <Switch
                checked={!!trunk.shareRoutes}
                onChange={(checked) => update(index, { shareRoutes: checked })}
              />
              <span className="text-xs text-slate-500 dark:text-slate-400">
                {/* Not a convenience toggle: it is the isolation this trunk's
                    own context provides, switched off. */}
                {t("同时允许使用分机路由（这些呼叫不受上面的 SIM 限制和并发上限约束）")}
              </span>
            </div>
          </div>
        ))}
      </div>

      <p className="mt-2 text-xs text-slate-500 dark:text-slate-400">
        {t("必须填写来源地址或呼入凭据之一；转发呼叫到对端需要呼出凭据；目标模式以 _ 开头")}
        {state?.path ? ` · ${state.path}` : ""}
      </p>

      {state?.preview ? (
        <details className="mt-2">
          <summary className="cursor-pointer text-xs text-sky-600 dark:text-sky-400">
            {t("生成的配置")}
          </summary>
          <pre className="mt-2 overflow-x-auto rounded-lg bg-slate-50 p-3 text-xs dark:bg-slate-800/50">
            {state.preview}
            {state.routesPreview ? `\n${state.routesPreview}` : ""}
          </pre>
        </details>
      ) : null}
    </div>
  );
}
