import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  CallRegular,
  CallEndRegular,
  MicRegular,
  MicOffRegular,
  SpeakerMuteRegular,
} from "@fluentui/react-icons";
import { apiMessage, api, dialCall, hangupCall, answerCall, listCalls } from "../api";
import type { Call, DeviceListItem } from "../types";
import {
  Button,
  EmptyState,
  Input,
  PageHeader,
  Select,
  StatusDot,
  Tag,
  message,
} from "../components/ui";
import type { StatusTone } from "../components/ui";
import { useCallAudio } from "../lib/useCallAudio";
import { usePolling } from "../lib/usePolling";
import { tf, useI18n } from "../lib/i18n";

// A call is finished once the server stamps ended_at. Terminal records linger
// for 30 seconds (terminalCallRetention in call_runtime.go) so the outcome
// stays readable after the far end hangs up.
function isLive(call: Call) {
  return !call.endedAt;
}

function stateTone(state: string): StatusTone {
  if (state === "active") return "success";
  if (state === "failed") return "danger";
  if (state === "ended") return "neutral";
  return "warning";
}

function elapsed(from: string | undefined, now: number): string {
  if (!from) return "—";
  const started = new Date(from).getTime();
  if (Number.isNaN(started)) return "—";
  const seconds = Math.max(0, Math.floor((now - started) / 1000));
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;
}

// Peak amplitude is only meaningful to a person as "is anything moving", so the
// meter trades accuracy for legibility: a square root lifts quiet speech into
// visible territory instead of leaving the bar flat.
function LevelMeter({ label, level, muted }: { label: string; level: number; muted?: boolean }) {
  const width = Math.min(100, Math.round(Math.sqrt(Math.max(0, level)) * 100));
  return (
    <div className="flex items-center gap-2">
      <span className="w-10 shrink-0 text-[11px] font-bold text-gray-400">{label}</span>
      <div className="h-1.5 flex-1 overflow-hidden rounded-full bg-gray-200 dark:bg-white/10">
        <div
          className={muted ? "h-full rounded-full bg-gray-400" : "h-full rounded-full bg-green-500"}
          style={{ width: `${muted ? 0 : width}%`, transition: "width 160ms linear" }}
        />
      </div>
      <span className="w-9 shrink-0 text-right font-mono text-[10px] text-gray-400">{width}%</span>
    </div>
  );
}

// getUserMedia is gated on a secure context, so an http:// origin other than
// localhost can never send audio. Read once: it cannot change while mounted.
const secureContext =
  typeof window !== "undefined" && window.isSecureContext && !!navigator.mediaDevices?.getUserMedia;

export default function CallsPage() {
  const { t } = useI18n();
  const [devices, setDevices] = useState<DeviceListItem[]>([]);
  const [deviceId, setDeviceId] = useState("");
  const [transport, setTransport] = useState("");
  const [calls, setCalls] = useState<Call[]>([]);
  const [number, setNumber] = useState("");
  const [busy, setBusy] = useState(false);
  const [loadError, setLoadError] = useState("");
  const [audioOn, setAudioOn] = useState(false);
  const [micOn, setMicOn] = useState(true);
  const [now, setNow] = useState(() => Date.now());
  const deviceRef = useRef(deviceId);
  deviceRef.current = deviceId;

  useEffect(() => {
    api<{ devices?: DeviceListItem[] }>("/devices")
      .then((result) => {
        const list = result.devices || [];
        setDevices(list);
        setDeviceId((current) => current || list[0]?.id || "");
      })
      .catch((error: unknown) => setLoadError(apiMessage(error)));
  }, []);

  const refresh = useCallback(() => {
    const target = deviceRef.current;
    if (!target) return;
    listCalls(target)
      .then((result) => {
        if (deviceRef.current !== target) return;
        setCalls(result.calls || []);
        setTransport(result.transport || "");
        setLoadError("");
      })
      .catch((error: unknown) => {
        if (deviceRef.current !== target) return;
        setLoadError(apiMessage(error));
      });
  }, []);

  // One second is fast enough to watch dialing → ringing → active unfold, and
  // slow enough that a stalled call is not hammering the modem.
  usePolling(refresh, 1000);
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, []);
  useEffect(() => {
    setCalls([]);
    refresh();
  }, [deviceId, refresh]);

  const active = useMemo(() => calls.find(isLive) || null, [calls]);
  const activeId = active?.id || "";
  const mediaReady = !!active?.mediaReady;

  // Audio can only attach once SDP has been negotiated. Dropping the toggle
  // when media goes away also tears the bridge down at the end of a call.
  useEffect(() => {
    if (!mediaReady) setAudioOn(false);
  }, [mediaReady, activeId]);

  const audio = useCallAudio(deviceId, activeId, audioOn && mediaReady, micOn);

  const run = async (action: () => Promise<unknown>, failure: string) => {
    setBusy(true);
    try {
      await action();
      refresh();
    } catch (error) {
      message.error(`${failure}：${apiMessage(error)}`);
    } finally {
      setBusy(false);
    }
  };

  const deviceOptions = devices.map((device) => ({ value: device.id, label: device.name || device.id }));

  return (
    <div className="p-4 sm:p-6">
      <PageHeader
        title={t("语音通话")}
        subtitle={t("通过 VoWiFi IMS 拨打电话，并把通话音频桥接到浏览器")}
      />

      <div className="grid gap-4 lg:grid-cols-2">
        <div className="ui-card p-4">
          <div className="mb-3 flex items-center gap-2">
            <div className="min-w-0 flex-1">
              <Select
                value={deviceId}
                onChange={setDeviceId}
                options={deviceOptions}
                placeholder={t("选择设备")}
              />
            </div>
            {transport ? (
              <Tag type={transport === "vowifi" ? "success" : "info"}>{transport}</Tag>
            ) : null}
          </div>

          {transport === "cellular" ? (
            <p className="mb-3 rounded-lg bg-amber-50 p-2 text-xs text-amber-700 dark:bg-amber-500/10 dark:text-amber-300">
              {t("IMS 未注册，将使用模组电路域拨号。电路域通话没有浏览器音频。")}
            </p>
          ) : null}

          <div className="flex items-end gap-2">
            <div className="min-w-0 flex-1">
              <Input
                value={number}
                onChange={(e) => setNumber(e.target.value)}
                placeholder={t("号码，例如 +8613800138000")}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && number.trim() && !active) {
                    void run(() => dialCall(deviceId, number.trim()), t("拨号失败"));
                  }
                }}
              />
            </div>
            {active ? (
              <Button
                variant="danger"
                loading={busy}
                icon={<CallEndRegular />}
                onClick={() => void run(() => hangupCall(deviceId, active.id), t("挂断失败"))}
              >
                {t("挂断")}
              </Button>
            ) : (
              <Button
                variant="primary"
                loading={busy}
                disabled={!deviceId || !number.trim()}
                icon={<CallRegular />}
                onClick={() => void run(() => dialCall(deviceId, number.trim()), t("拨号失败"))}
              >
                {t("拨号")}
              </Button>
            )}
          </div>

          {active && active.direction === "incoming" && active.state === "ringing" ? (
            <Button
              variant="success"
              className="mt-3 w-full"
              loading={busy}
              icon={<CallRegular />}
              onClick={() => void run(() => answerCall(deviceId, active.id), t("接听失败"))}
            >
              {tf("接听 {number}", { number: active.number })}
            </Button>
          ) : null}

          {loadError ? <p className="mt-3 text-xs text-red-500">{loadError}</p> : null}
        </div>

        <div className="ui-card p-4">
          {active ? (
            <>
              <div className="flex items-center gap-2">
                <StatusDot tone={stateTone(active.state)} />
                <span className="font-mono text-lg font-bold">{active.number || "—"}</span>
                <Tag type="info">{active.direction}</Tag>
                <span className="ml-auto font-mono text-sm text-gray-400">
                  {elapsed(active.answeredAt || active.startedAt, now)}
                </span>
              </div>

              <dl className="mt-3 grid grid-cols-2 gap-x-4 gap-y-1 text-xs">
                <dt className="text-gray-400">{t("状态")}</dt>
                <dd className="font-mono">{active.state}</dd>
                <dt className="text-gray-400">{t("最后响应")}</dt>
                <dd className="font-mono">{active.sipCode ? `${active.sipCode} ${active.reason || ""}` : "—"}</dd>
                <dt className="text-gray-400">{t("接通时间")}</dt>
                <dd className="font-mono">
                  {active.answeredAt ? new Date(active.answeredAt).toLocaleTimeString() : t("未接通")}
                </dd>
                <dt className="text-gray-400">{t("编解码")}</dt>
                <dd className="font-mono">{active.codec || "—"}</dd>
                <dt className="text-gray-400">{t("媒体就绪")}</dt>
                <dd className="font-mono">{active.mediaReady ? "true" : "false"}</dd>
              </dl>

              <div className="mt-4 border-t border-gray-100 pt-3 dark:border-white/10">
                <div className="flex items-center gap-2">
                  <Button
                    variant={audioOn ? "danger" : "primary"}
                    size="small"
                    disabled={!mediaReady}
                    icon={audioOn ? <SpeakerMuteRegular /> : <CallRegular />}
                    onClick={() => setAudioOn((value) => !value)}
                  >
                    {audioOn ? t("断开音频") : t("连接音频")}
                  </Button>
                  <Button
                    variant={micOn ? "default" : "warning"}
                    size="small"
                    disabled={!audioOn}
                    icon={micOn ? <MicRegular /> : <MicOffRegular />}
                    onClick={() => setMicOn((value) => !value)}
                  >
                    {micOn ? t("静音") : t("取消静音")}
                  </Button>
                  <span className="ml-auto font-mono text-[11px] text-gray-400">{audio.state}</span>
                </div>

                {!mediaReady ? (
                  <p className="mt-2 text-xs text-gray-400">
                    {t("媒体尚未协商，收到带 SDP 的响应后即可连接音频。")}
                  </p>
                ) : null}

                {/* Worth saying before the call rather than after: on an
                    insecure origin the browser withholds the microphone
                    entirely, so the call is receive-only however healthy it
                    otherwise looks. */}
                {!secureContext ? (
                  <p className="mt-2 rounded-lg bg-amber-50 p-2 text-xs text-amber-700 dark:bg-amber-500/10 dark:text-amber-300">
                    {t("当前页面不是安全上下文，浏览器不会提供麦克风，通话将只能接收。请改用 HTTPS（设置中可开启自签名证书）或通过 http://localhost 访问。")}
                  </p>
                ) : null}

                {audioOn ? (
                  <div className="mt-3 space-y-2">
                    <LevelMeter label={t("下行")} level={audio.rxLevel} />
                    <LevelMeter label={t("上行")} level={audio.txLevel} muted={!micOn} />
                    <p className="font-mono text-[10px] text-gray-400">
                      {tf("已接收 {rx} 帧 · 已发送 {tx} 帧", { rx: audio.received, tx: audio.sent })}
                    </p>
                    {audio.error ? <p className="text-xs text-red-500">{audio.error}</p> : null}
                  </div>
                ) : null}
              </div>
            </>
          ) : (
            <EmptyState title={t("当前没有通话")} subtitle={t("拨号后这里会显示通话状态与音频桥接")} />
          )}
        </div>
      </div>

      {calls.length > 0 ? (
        <div className="ui-card mt-4 overflow-x-auto">
          <table className="w-full min-w-[640px] text-sm">
            <thead className="border-b border-gray-200/70 text-left text-xs uppercase text-gray-500 dark:border-white/10 dark:text-gray-400">
              <tr>
                <th className="p-3">{t("号码")}</th>
                <th className="p-3">{t("方向")}</th>
                <th className="p-3">{t("状态")}</th>
                <th className="p-3">SIP</th>
                <th className="p-3">{t("编解码")}</th>
                <th className="p-3">{t("开始")}</th>
              </tr>
            </thead>
            <tbody>
              {calls.map((call) => (
                <tr key={call.id} className="border-b border-gray-100 last:border-0 dark:border-white/5">
                  <td className="p-3 font-mono">{call.number || "—"}</td>
                  <td className="p-3 text-gray-500">{call.direction}</td>
                  <td className="p-3">
                    <span className="inline-flex items-center gap-1.5">
                      <StatusDot tone={stateTone(call.state)} animated={isLive(call)} />
                      {call.state}
                    </span>
                  </td>
                  <td className="p-3 font-mono text-xs text-gray-500">
                    {call.sipCode ? `${call.sipCode} ${call.reason || ""}` : "—"}
                  </td>
                  <td className="p-3 font-mono text-xs">{call.codec || "—"}</td>
                  <td className="p-3 text-xs text-gray-400">{new Date(call.startedAt).toLocaleTimeString()}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}
    </div>
  );
}
