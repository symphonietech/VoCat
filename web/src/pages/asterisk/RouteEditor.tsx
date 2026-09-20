import { useCallback, useEffect, useState } from "react";
import { AddRegular, DeleteRegular, ArrowSyncRegular } from "@fluentui/react-icons";
import { apiMessage, getAsteriskRoutes, saveAsteriskRoutes, applyAsteriskRoutes } from "../../api";
import type { AsteriskRoute, AsteriskRoutes } from "../../types";
import { Button, Input, Tag, message } from "../../components/ui";
import { useI18n } from "../../lib/i18n";

const DEFAULT_TIMEOUT = 60;

function blankRoute(): AsteriskRoute {
  return { pattern: "_", devices: [], timeoutSeconds: DEFAULT_TIMEOUT, comment: "" };
}

export function RouteEditor() {
  const { t } = useI18n();
  const [state, setState] = useState<AsteriskRoutes | null>(null);
  const [routes, setRoutes] = useState<AsteriskRoute[]>([]);
  const [dirty, setDirty] = useState(false);
  const [busy, setBusy] = useState(false);

  const load = useCallback((keepEdits: boolean) => {
    getAsteriskRoutes()
      .then((next) => {
        setState(next);
        // Server state replaces local edits only when there are none to lose.
        if (!keepEdits) setRoutes(next.routes ?? []);
      })
      .catch((error) => message.error(apiMessage(error)));
  }, []);

  useEffect(() => {
    load(false);
  }, [load]);

  const update = (index: number, patch: Partial<AsteriskRoute>) => {
    setRoutes((current) =>
      current.map((route, at) => (at === index ? { ...route, ...patch } : route)),
    );
    setDirty(true);
  };

  const save = async () => {
    setBusy(true);
    try {
      await saveAsteriskRoutes(routes);
      setDirty(false);
      message.success(t("已保存"));
      load(false);
    } catch (error) {
      // The server refuses a route rather than storing one that could never
      // be applied, and names the offending rule.
      message.error(apiMessage(error));
    } finally {
      setBusy(false);
    }
  };

  const apply = async () => {
    setBusy(true);
    try {
      const result = await applyAsteriskRoutes();
      message.success(result.message || t("已应用"));
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
        <h2 className="text-sm font-medium text-slate-600 dark:text-slate-300">{t("分机路由")}</h2>
        {dirty ? <Tag type="warning">{t("未保存")}</Tag> : null}
        {!dirty && state?.pending ? <Tag type="warning">{t("待应用")}</Tag> : null}
        <div className="ml-auto flex flex-wrap gap-2">
          <Button
            icon={<AddRegular />}
            onClick={() => {
              setRoutes((current) => [...current, blankRoute()]);
              setDirty(true);
            }}
          >
            {t("添加路由")}
          </Button>
          <Button variant="primary" loading={busy} disabled={!dirty} onClick={save}>
            {t("保存")}
          </Button>
          <Button
            icon={<ArrowSyncRegular />}
            loading={busy}
            // Applying an unsaved edit would reload the previous rules and
            // report success, which is worse than refusing to run.
            disabled={dirty || !state?.canApply}
            onClick={apply}
          >
            {t("应用")}
          </Button>
        </div>
      </div>

      {state?.unknownDevices?.length ? (
        <p className="mb-2 rounded-lg bg-amber-50 p-2 text-xs text-amber-700 dark:bg-amber-500/10 dark:text-amber-300">
          {t("这些设备已不存在，使用它们的路由在拨号时会失败")}: {state.unknownDevices.join(", ")}
        </p>
      ) : null}

      {state && !state.canApply ? (
        <p className="mb-2 rounded-lg bg-amber-50 p-2 text-xs text-amber-700 dark:bg-amber-500/10 dark:text-amber-300">
          {t("未配置管理接口，保存后需手动重启 Asterisk 容器才能生效")}
        </p>
      ) : null}

      <div className="ui-card divide-y divide-slate-100 dark:divide-slate-800">
        {routes.length === 0 ? (
          <p className="p-3 text-sm text-slate-500 dark:text-slate-400">
            {t("没有分机路由。没有任何路由时，分机的外呼都会被 Asterisk 拒绝。")}
          </p>
        ) : null}
        {routes.map((route, index) => (
          <div key={index} className="grid gap-2 p-3 sm:grid-cols-[1fr_2fr_5rem_auto]">
            <Input
              value={route.pattern}
              placeholder="_1NXXNXXXXXX"
              onChange={(event) => update(index, { pattern: event.target.value })}
            />
            <Input
              value={route.devices.join(" ")}
              placeholder={t("设备 ID，用空格分隔")}
              onChange={(event) =>
                update(index, { devices: event.target.value.split(/[\s,]+/).filter(Boolean) })
              }
            />
            <Input
              value={String(route.timeoutSeconds)}
              onChange={(event) =>
                update(index, {
                  timeoutSeconds: Number(event.target.value.replace(/\D/g, "")) || 0,
                })
              }
            />
            <Button
              icon={<DeleteRegular />}
              onClick={() => {
                setRoutes((current) => current.filter((_, at) => at !== index));
                setDirty(true);
              }}
            />
          </div>
        ))}
      </div>

      <p className="mt-2 text-xs text-slate-500 dark:text-slate-400">
        {t("模式必须以 _ 开头；多个设备轮流拨出；超时单位为秒")}
        {state?.path ? ` · ${state.path}` : ""}
      </p>

      {state?.preview ? (
        <details className="mt-2">
          <summary className="cursor-pointer text-xs text-sky-600 dark:text-sky-400">
            {t("生成的拨号方案")}
          </summary>
          <pre className="mt-2 overflow-x-auto rounded-lg bg-slate-50 p-3 text-xs dark:bg-slate-800/50">
            {state.preview}
          </pre>
        </details>
      ) : null}
    </div>
  );
}
