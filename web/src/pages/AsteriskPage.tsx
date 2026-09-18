import { useCallback, useState } from "react";
import { ServerRegular, ArrowClockwiseRegular } from "@fluentui/react-icons";
import { getAsteriskStatus } from "../api";
import type { AsteriskEndpoint, AsteriskStatus } from "../types";
import { Button, EmptyState, PageHeader, StatusDot, Tag } from "../components/ui";
import type { StatusTone } from "../components/ui";
import { usePolling } from "../lib/usePolling";
import { useI18n } from "../lib/i18n";

// The trunk endpoint is the one VoCat itself answers. Naming it here keeps
// the page honest about which row is the SIM path rather than a phone.
const TRUNK_ENDPOINT = "vocat";

function endpointTone(endpoint: AsteriskEndpoint): StatusTone {
  if (endpoint.reachable) return "success";
  if (endpoint.registered) return "warning";
  return "neutral";
}

// Linphone packs its APNs push token into the contact URI, which runs to
// several hundred characters and buries the part anyone reads. The
// parameters stay available under Raw fields.
function shortURI(uri: string | undefined) {
  if (!uri) return "";
  const [base] = uri.split(";");
  return base;
}

function hasParameters(uri: string | undefined) {
  return Boolean(uri && uri.includes(";"));
}

function contactTone(status: string | undefined): StatusTone {
  if (!status) return "neutral";
  if (/^reachable$/i.test(status)) return "success";
  if (/^(unreachable|removed)$/i.test(status)) return "danger";
  return "warning";
}

export default function AsteriskPage() {
  const { t } = useI18n();
  const [status, setStatus] = useState<AsteriskStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [expanded, setExpanded] = useState<string>("");

  const refresh = useCallback(() => {
    getAsteriskStatus()
      .then((next) => setStatus(next))
      .catch(() => {
        // A transport failure here is VoCat's own API, not the PBX. Leaving
        // the last good snapshot up beats blanking the page on one blip.
      })
      .finally(() => setLoading(false));
  }, []);

  usePolling(refresh, 5000);

  const endpoints = status?.endpoints ?? [];
  const trunk = endpoints.find((entry) => entry.name === TRUNK_ENDPOINT);
  const phones = endpoints.filter((entry) => entry.name !== TRUNK_ENDPOINT);

  return (
    <div className="p-4 sm:p-6">
      <PageHeader
        title={t("Asterisk")}
        subtitle={t("查看前置 PBX 的实时状态：分机注册情况与中继可达性")}
        actions={
          <Button icon={<ArrowClockwiseRegular />} onClick={refresh}>
            {t("刷新")}
          </Button>
        }
      />

      {!loading && status && !status.configured ? (
        <div className="ui-card p-4">
          <EmptyState
            icon={<ServerRegular />}
            title={t("未配置 Asterisk 管理接口")}
            subtitle={t(
              "设置 ASTERISK_AMI_SECRET 后重建容器即可启用。未配置时 VoCat 完全正常工作，只是无法读取 PBX 状态。",
            )}
          />
        </div>
      ) : null}

      {status?.configured && status.error ? (
        <div className="ui-card mb-4 p-4">
          <div className="mb-1 flex items-center gap-2">
            <StatusDot tone="danger" />
            <span className="font-medium">{t("无法连接到 Asterisk")}</span>
          </div>
          <p className="break-all text-xs text-slate-500 dark:text-slate-400">{status.error}</p>
        </div>
      ) : null}

      {status?.reachable ? (
        <>
          <div className="ui-card mb-4 p-4">
            <div className="flex flex-wrap items-center gap-3">
              <StatusDot tone="success" />
              <span className="font-medium">{t("已连接")}</span>
              {status.version ? <Tag type="info">{status.version}</Tag> : null}
              {status.address ? (
                <span className="text-xs text-slate-500 dark:text-slate-400">{status.address}</span>
              ) : null}
              {status.core?.calls ? (
                <Tag type="info">
                  {t("进行中通话")}: {status.core.calls}
                </Tag>
              ) : null}
            </div>
            {status.core?.reloadTime ? (
              <p className="mt-2 text-xs text-slate-500 dark:text-slate-400">
                {t("最近一次重载")}: {status.core.reloadTime}
              </p>
            ) : null}
          </div>

          <Section title={t("中继（VoCat）")}>
            {trunk ? (
              <EndpointRow
                endpoint={trunk}
                expanded={expanded}
                onToggle={setExpanded}
                t={t}
              />
            ) : (
              <p className="p-3 text-sm text-slate-500 dark:text-slate-400">
                {t("Asterisk 中没有名为 vocat 的中继端点")}
              </p>
            )}
          </Section>

          <Section title={t("分机")}>
            {phones.length ? (
              phones.map((endpoint) => (
                <EndpointRow
                  key={endpoint.name}
                  endpoint={endpoint}
                  expanded={expanded}
                  onToggle={setExpanded}
                  t={t}
                />
              ))
            ) : (
              <p className="p-3 text-sm text-slate-500 dark:text-slate-400">{t("没有分机")}</p>
            )}
          </Section>

          {status.unpairedContacts?.length ? (
            <Section title={t("未匹配到端点的联系地址")}>
              {status.unpairedContacts.map((contact, index) => (
                <div
                  key={contact.uri ?? index}
                  className="flex flex-wrap items-center gap-2 p-3 text-xs text-slate-500 dark:text-slate-400"
                >
                  <StatusDot tone={contactTone(contact.status)} />
                  <span className="break-all" title={contact.uri}>
                    {shortURI(contact.uri)}
                  </span>
                  {contact.status ? <span>{contact.status}</span> : null}
                </div>
              ))}
            </Section>
          ) : null}

          {status.contactsError ? (
            <p className="mt-3 text-xs text-amber-600 dark:text-amber-400">
              {t("无法读取注册信息")}: {status.contactsError}
            </p>
          ) : null}
        </>
      ) : null}
    </div>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="mb-4">
      <h2 className="mb-2 text-sm font-medium text-slate-600 dark:text-slate-300">{title}</h2>
      <div className="ui-card divide-y divide-slate-100 dark:divide-slate-800">{children}</div>
    </div>
  );
}

function EndpointRow({
  endpoint,
  expanded,
  onToggle,
  t,
}: {
  endpoint: AsteriskEndpoint;
  expanded: string;
  onToggle: (name: string) => void;
  t: (key: string) => string;
}) {
  const open = expanded === endpoint.name;
  return (
    <div className="p-3">
      <div className="flex flex-wrap items-center gap-2">
        <StatusDot tone={endpointTone(endpoint)} />
        <span className="font-medium">{endpoint.name}</span>
        {endpoint.state ? <Tag type="info">{endpoint.state}</Tag> : null}
        {endpoint.name === TRUNK_ENDPOINT ? (
          // A trunk has a configured contact and never registers, so
          // reachability is the only meaningful claim about it.
          <Tag type={endpoint.reachable ? "success" : endpoint.registered ? "warning" : "danger"}>
            {endpoint.reachable
              ? t("可达")
              : endpoint.registered
                ? t("未验证可达性")
                : t("无联系地址")}
          </Tag>
        ) : endpoint.registered ? (
          <Tag type="success">{t("已注册")}</Tag>
        ) : (
          <Tag type="warning">{t("未注册")}</Tag>
        )}
        {endpoint.activeChannels && endpoint.activeChannels !== "0" ? (
          <Tag type="warning">
            {t("通道")}: {endpoint.activeChannels}
          </Tag>
        ) : null}
        <button
          type="button"
          className="ml-auto text-xs text-sky-600 hover:underline dark:text-sky-400"
          onClick={() => onToggle(open ? "" : endpoint.name)}
        >
          {open ? t("收起") : t("原始字段")}
        </button>
      </div>

      {endpoint.contacts.map((contact, index) => (
        <div
          key={contact.uri ?? index}
          className="mt-2 flex flex-wrap items-center gap-2 pl-4 text-xs text-slate-500 dark:text-slate-400"
        >
          <StatusDot tone={contactTone(contact.status)} />
          <span className="break-all" title={contact.uri}>
            {shortURI(contact.uri)}
          </span>
          {hasParameters(contact.uri) ? (
            <span className="text-slate-400 dark:text-slate-500">
              {t("（含推送参数）")}
            </span>
          ) : null}
          {contact.status ? <span>{contact.status}</span> : null}
          {contact.roundtripMs ? <span>{contact.roundtripMs.toFixed(1)} ms</span> : null}
          {/* Which phone, and from where -- the two things you actually want
              when a registration looks wrong. */}
          {contact.userAgent ? <span className="truncate">{contact.userAgent}</span> : null}
          {contact.viaAddress ? <span>{contact.viaAddress}</span> : null}
        </div>
      ))}

      {open ? (
        // Asterisk renames manager fields between versions, so VoCat shows
        // what it actually received rather than only the keys it recognises.
        <dl className="mt-3 grid grid-cols-1 gap-x-4 gap-y-1 rounded-lg bg-slate-50 p-3 text-xs sm:grid-cols-2 dark:bg-slate-800/50">
          {(endpoint.fields ?? []).map((field) => (
            <div key={field.name} className="flex gap-2">
              <dt className="shrink-0 text-slate-500 dark:text-slate-400">{field.name}</dt>
              <dd className="break-all text-slate-700 dark:text-slate-200">{field.value}</dd>
            </div>
          ))}
        </dl>
      ) : null}
    </div>
  );
}
