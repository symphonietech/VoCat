import { useState } from "react";
import { CallEndRegular } from "@fluentui/react-icons";
import { apiMessage, hangupAsteriskChannel } from "../../api";
import type { AsteriskChannel } from "../../types";
import { Button, StatusDot, Tag, message } from "../../components/ui";
import type { StatusTone } from "../../components/ui";
import { useI18n } from "../../lib/i18n";

// Up is a connected leg; Ring and Ringing are one that has not been answered.
// Anything else is a leg in some transitional state, which is worth seeing but
// not worth colouring as healthy.
function channelTone(state: string | undefined): StatusTone {
  if (/^up$/i.test(state ?? "")) return "success";
  if (/^ring/i.test(state ?? "")) return "warning";
  return "neutral";
}

export function ChannelList({
  channels,
  error,
  onChange,
}: {
  channels: AsteriskChannel[];
  error?: string;
  onChange: () => void;
}) {
  const { t } = useI18n();
  const [busy, setBusy] = useState("");

  const hangup = async (channel: string) => {
    setBusy(channel);
    try {
      await hangupAsteriskChannel(channel);
      message.success(t("已挂断"));
      onChange();
    } catch (problem) {
      // A channel that ended between the poll and the click is the common
      // case, and the server says so rather than failing vaguely.
      message.error(apiMessage(problem));
      onChange();
    } finally {
      setBusy("");
    }
  };

  return (
    <div className="mb-4">
      <div className="mb-2 flex flex-wrap items-center gap-2">
        <h2 className="text-sm font-medium text-slate-600 dark:text-slate-300">{t("进行中的通道")}</h2>
        <span className="text-xs text-slate-400">{channels.length}</span>
      </div>

      {error ? (
        <p className="mb-2 text-xs text-amber-600 dark:text-amber-400">
          {t("无法读取通道")}: {error}
        </p>
      ) : null}

      <div className="ui-card divide-y divide-slate-100 dark:divide-slate-800">
        {channels.length === 0 ? (
          <p className="p-3 text-sm text-slate-500 dark:text-slate-400">{t("当前没有通话")}</p>
        ) : null}
        {channels.map((channel) => (
          <div key={channel.name} className="flex flex-wrap items-center gap-2 p-3 text-sm">
            <StatusDot tone={channelTone(channel.state)} />
            <span className="font-mono text-xs">{channel.name}</span>
            {channel.state ? <Tag type="info">{channel.state}</Tag> : null}
            {channel.callerId ? <span className="text-xs">{channel.callerId}</span> : null}
            {channel.connectedLine ? (
              <span className="text-xs text-slate-500 dark:text-slate-400">
                → {channel.connectedLine}
              </span>
            ) : null}
            {channel.application ? (
              <span className="text-xs text-slate-400">{channel.application}</span>
            ) : null}
            <span className="ml-auto font-mono text-xs text-slate-400">{channel.duration || "—"}</span>
            <Button
              variant="danger"
              size="small"
              icon={<CallEndRegular />}
              loading={busy === channel.name}
              onClick={() => void hangup(channel.name)}
            >
              {t("挂断")}
            </Button>
          </div>
        ))}
      </div>
    </div>
  );
}
