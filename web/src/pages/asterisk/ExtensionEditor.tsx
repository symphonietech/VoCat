import { useCallback, useEffect, useState } from "react";
import { AddRegular, DeleteRegular, ArrowSyncRegular, KeyRegular } from "@fluentui/react-icons";
import {
  apiMessage,
  getAsteriskExtensions,
  saveAsteriskExtensions,
  applyAsteriskExtensions,
} from "../../api";
import type { AsteriskExtension, AsteriskExtensions } from "../../types";
import { Button, Input, Tag, message } from "../../components/ui";
import { useI18n } from "../../lib/i18n";

const DEFAULT_CONTACTS = 2;

function blankExtension(): AsteriskExtension {
  return { name: "", password: "", callerId: "", maxContacts: DEFAULT_CONTACTS };
}

// A generated password is the safe default for an account that answers a
// public registrar. Built from the browser's CSPRNG and from an alphabet with
// no ambiguous characters, because this gets typed into a handset by hand.
const ALPHABET = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789";

function generatePassword(length = 20) {
  const bytes = new Uint32Array(length);
  crypto.getRandomValues(bytes);
  return Array.from(bytes, (value) => ALPHABET[value % ALPHABET.length]).join("");
}

export function ExtensionEditor() {
  const { t } = useI18n();
  const [state, setState] = useState<AsteriskExtensions | null>(null);
  const [extensions, setExtensions] = useState<AsteriskExtension[]>([]);
  const [dirty, setDirty] = useState(false);
  const [busy, setBusy] = useState(false);

  const load = useCallback((keepEdits: boolean) => {
    getAsteriskExtensions()
      .then((next) => {
        setState(next);
        if (!keepEdits) setExtensions(next.extensions ?? []);
      })
      .catch((error) => message.error(apiMessage(error)));
  }, []);

  useEffect(() => {
    load(false);
  }, [load]);

  const update = (index: number, patch: Partial<AsteriskExtension>) => {
    setExtensions((current) =>
      current.map((extension, at) => (at === index ? { ...extension, ...patch } : extension)),
    );
    setDirty(true);
  };

  const save = async (replaceSeeded = false) => {
    setBusy(true);
    try {
      await saveAsteriskExtensions(extensions, replaceSeeded);
      setDirty(false);
      // Passwords are write-only, so what was typed cannot be read back --
      // say so at the moment it stops being visible rather than later.
      message.success(t("已保存。密码只写入 Asterisk，之后无法再读取。"));
      load(false);
    } catch (error) {
      // 409 is the deliberate guard against silently removing the account the
      // container seeded from .env. Asking again is the whole point of it.
      if ((error as { status?: number })?.status === 409) {
        if (window.confirm(apiMessage(error))) {
          await save(true);
          return;
        }
        return;
      }
      message.error(apiMessage(error));
    } finally {
      setBusy(false);
    }
  };

  const apply = async () => {
    setBusy(true);
    try {
      const result = await applyAsteriskExtensions();
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
        <h2 className="text-sm font-medium text-slate-600 dark:text-slate-300">{t("分机账号")}</h2>
        {dirty ? <Tag type="warning">{t("未保存")}</Tag> : null}
        {!dirty && state?.pending ? <Tag type="warning">{t("待应用")}</Tag> : null}
        <div className="ml-auto flex flex-wrap gap-2">
          <Button
            icon={<AddRegular />}
            onClick={() => {
              setExtensions((current) => [...current, blankExtension()]);
              setDirty(true);
            }}
          >
            {t("添加分机")}
          </Button>
          <Button variant="primary" loading={busy} disabled={!dirty} onClick={() => save()}>
            {t("保存")}
          </Button>
          <Button
            icon={<ArrowSyncRegular />}
            loading={busy}
            disabled={dirty || !state?.canApply}
            onClick={apply}
          >
            {t("应用")}
          </Button>
        </div>
      </div>

      {state?.seeded ? (
        <p className="mb-2 rounded-lg bg-amber-50 p-2 text-xs text-amber-700 dark:bg-amber-500/10 dark:text-amber-300">
          {t("当前分机来自 .env（ASTERISK_SIP_USER），不是在这里配置的。在此保存会整体替换它。")}
        </p>
      ) : null}

      {state && !state.canApply ? (
        <p className="mb-2 rounded-lg bg-amber-50 p-2 text-xs text-amber-700 dark:bg-amber-500/10 dark:text-amber-300">
          {t("未配置管理接口，保存后需手动重启 Asterisk 容器才能生效")}
        </p>
      ) : null}

      <div className="ui-card divide-y divide-slate-100 dark:divide-slate-800">
        {extensions.length === 0 ? (
          <p className="p-3 text-sm text-slate-500 dark:text-slate-400">
            {t("没有分机。没有分机时，软电话无法注册，但中继外呼不受影响。")}
          </p>
        ) : null}
        {extensions.map((extension, index) => (
          <div key={index} className="grid gap-2 p-3 sm:grid-cols-[8rem_1fr_8rem_4rem_auto]">
            <Input
              value={extension.name}
              placeholder="1001"
              onChange={(event) => update(index, { name: event.target.value })}
            />
            <div className="flex gap-1">
              <Input
                value={extension.password ?? ""}
                // Not a password field: it is write-only, so this is the only
                // moment anyone can read what was set, and it has to be typed
                // into a handset from here.
                placeholder={
                  extension.hasPassword ? t("已设置，留空则不修改") : t("密码（留空则无法创建）")
                }
                onChange={(event) => update(index, { password: event.target.value })}
              />
              <Button
                icon={<KeyRegular />}
                title={t("生成密码")}
                onClick={() => update(index, { password: generatePassword() })}
              />
            </div>
            <Input
              value={extension.callerId ?? ""}
              placeholder={t("显示名")}
              onChange={(event) => update(index, { callerId: event.target.value })}
            />
            <Input
              value={String(extension.maxContacts)}
              title={t("同时注册的设备数")}
              onChange={(event) =>
                update(index, {
                  maxContacts: Number(event.target.value.replace(/\D/g, "")) || 0,
                })
              }
            />
            <Button
              icon={<DeleteRegular />}
              onClick={() => {
                setExtensions((current) => current.filter((_, at) => at !== index));
                setDirty(true);
              }}
            />
          </div>
        ))}
      </div>

      <p className="mt-2 text-xs text-slate-500 dark:text-slate-400">
        {t("密码至少")} {state?.minPasswordLength ?? 12}{" "}
        {t("位，保存后不再回显；设备数是可同时注册的终端数量。分机之间可直接互拨。")}
        {state?.path ? ` · ${state.path}` : ""}
      </p>

      {state?.preview ? (
        <details className="mt-2">
          <summary className="cursor-pointer text-xs text-sky-600 dark:text-sky-400">
            {t("生成的 PJSIP 配置")}
          </summary>
          <pre className="mt-2 overflow-x-auto rounded-lg bg-slate-50 p-3 text-xs dark:bg-slate-800/50">
            {state.preview}
          </pre>
        </details>
      ) : null}

      {state?.internalPreview ? (
        // The half that answers "why can 1001 not reach 1003". Generated from
        // the same list, so an account cannot exist without being dialable.
        <details className="mt-2">
          <summary className="cursor-pointer text-xs text-sky-600 dark:text-sky-400">
            {t("生成的内线拨号方案")}
          </summary>
          <pre className="mt-2 overflow-x-auto rounded-lg bg-slate-50 p-3 text-xs dark:bg-slate-800/50">
            {state.internalPreview}
          </pre>
        </details>
      ) : null}
    </div>
  );
}
