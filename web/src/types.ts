export type ApiStatus = "ok" | "error";

export type DeviceType = "wifi_410" | "dji_4g" | "pcie_ec20_ec25" | "usb_sim_reader";

export interface Session {
  authenticated: boolean;
  username: string;
  role: string;
  expiresAt?: string;
  csrfToken?: string;
}

export interface LoginResponse {
  status: ApiStatus;
  username?: string;
  role?: string;
  expiresAt?: string;
  csrfToken?: string;
}

export interface ApiErrorBody {
  status?: string;
  code?: string;
  error?: string;
  message?: string;
  requestId?: string;
  warning?: string;
  busy?: boolean;
}

export interface VoWiFiRuntime {
  deviceId: string;
  phase: string;
  enabled?: boolean;
  active?: boolean;
  carrierProfile?: string;
  carrierProfileFrom?: string;
  dataplaneMode: string;
  iccid: string;
  imsi: string;
  simReady: boolean;
  accessReady: boolean;
  tunnelReady: boolean;
  imsReady: boolean;
  smsReady: boolean;
  regStatus: number;
  regStatusText: string;
  networkMode: string;
  localPhone?: string;
  phoneNumberSource?: string;
  lastErrorClass: string;
  lastError: string;
  lastReason: string;
  updatedAt: string;
  tunnel?: Record<string, unknown>;
  imscore?: Record<string, unknown>;
  smsip?: Record<string, unknown>;
}

export interface ModemSummary {
  operator: string;
  nativeMcc: string;
  nativeMnc: string;
  operatorCountryCode?: string;
  nativeSpn?: string;
  cardMcc?: string;
  cardMnc?: string;
  cardCountry?: string;
  homeCarrierName?: string;
  homeCarrierPlmn?: string;
  homeCarrierCountryCode?: string;
  serviceBlocked?: boolean;
  blockedReason?: string;
  networkMode: string;
  networkDuplex?: string;
  radioBand: string;
  radioChannel: number;
  signalDbm: number;
  signalRsrp?: number;
  signalRsrq?: number;
  signalSinr: number;
  imei: string;
  iccid: string;
  imsi?: string;
  firmware?: string;
  model?: string;
  regStatus: number;
  regStatusText?: string;
  psAttached?: boolean;
  simInserted?: boolean;
  operatingMode?: number;
  phoneNumber?: string;
  phoneNumberSource?: string;
}

export interface PublicIPInfo {
  detected?: boolean;
  ip: string;
  countryCode: string;
  region?: string;
  city?: string;
  organization?: string;
}

export interface SMSStorageArea {
  used?: number;
  total?: number;
}

export interface SMSStorageUsage {
  sm?: SMSStorageArea;
  me?: SMSStorageArea;
}

export interface SMSSettings {
  autoClearModemStorage: boolean;
}

export interface DeviceListItem {
  id: string;
  name: string;
  deviceType: DeviceType;
  running: boolean;
  healthy: boolean;
  controlOnline: boolean;
  physicalPresent?: boolean;
  workerRunning?: boolean;
  dataConnected?: boolean;
  radioRegistered?: boolean;
  lifecyclePhase?: string;
  lifecycleReason?: string;
  publicIp: string;
  privateIp?: string;
  interface: string;
  esimTransport: string;
  smsEnabled: boolean;
  smsStorage?: SMSStorageUsage;
	  networkEnabled: boolean;
  vowifiEnabled: boolean;
  vowifiActive?: boolean;
  vowifiRuntime: VoWiFiRuntime;
  modem: ModemSummary;
  networkConnected: boolean;
	  networkPhase?: "unknown" | "starting" | "connected" | "stopping" | "recovering" | "disabled" | "failed";
	  networkError?: string;
	  modemPhase?: "rebooting" | "";
	  publicIpInfo?: PublicIPInfo;
  registrationStateLabel: "registered" | "searching" | "denied" | "unknown";
  flightMode?: boolean;
}

export interface DevicesResponse {
  deviceLimit: number;
  devices: DeviceListItem[];
}

export interface DashboardDevice {
  id: string;
  name: string;
  deviceType: DeviceType;
  interface: string;
  proxyPort: number;
  publicIp: string;
  healthy: boolean;
  operator: string;
  signalDbm: number;
  networkMode: string;
  networkDuplex?: string;
  vowifiActive: boolean;
  vowifiRuntime: VoWiFiRuntime;
  networkConnected: boolean;
  model?: string;
}

export interface DashboardHostInfo {
  cpuModel: string;
  boardModel: string;
  memoryModel: string;
  diskModel: string;
}

export interface DashboardHostPerf {
  cpuPercent: number;
  memoryPercent: number;
  memoryUsedBytes: number;
  memoryTotalBytes: number;
  diskPercent: number;
  diskUsedBytes: number;
  diskTotalBytes: number;
  netRxBps: number;
  netTxBps: number;
}

export interface DashboardHost {
  host: DashboardHostInfo;
  perf: DashboardHostPerf;
}

// 仪表盘定时任务卡只关心名字与下次执行时间。
export interface DashboardUpcomingTask {
  id: number;
  name: string;
  enabled: boolean;
  nextRunAt: string;
}

export interface DeviceOverview extends DeviceListItem {
  atPort?: string;
  audioDevice?: string;
  backendMode?: string;
  controlDevice?: string;
  radioLiveOk?: boolean | null;
  traffic?: Record<string, string>;
  trafficRaw?: Record<string, number>;
  trafficMeta?: Record<string, unknown>;
}

export interface DeviceStatus {
  id: string;
  name: string;
  healthy: boolean;
  interface: string;
  publicIp: string;
  proxyPort: number;
  lastHardwareRefresh?: string;
  networkConnected: boolean;
  modem: ModemSummary;
  vowifi?: Record<string, unknown>;
  simServiceTable?: Record<string, unknown>;
  pnn?: Array<Record<string, unknown>>;
  opl?: Array<Record<string, unknown>>;
}

export interface DiscoveredDevice {
	 hardwareKind?: string;
	 readerName?: string;
  deviceType?: DeviceType;
  discoveryKey: string;
  controlPath: string;
  netInterface: string;
  usbPath: string;
  vendorId: number;
  productId: number;
  driverName: string;
  atPorts: string[];
  atPort: string;
  audioDevice?: string;
  imei?: string;
  mode: string;
  networkCapable: boolean;
  configured: boolean;
  configuredId?: string;
  degraded?: boolean;
  discoveryIssue?: "pcsc_service_unavailable" | "pcsc_driver_missing" | string;
  usbnetMode?: number | null;
}

export interface DeviceConfig {
  id: string;
  name: string;
  deviceType: DeviceType;
  interface: string;
  controlDevice: string;
  atPort: string;
  usbPath: string;
  audioDevice?: string;
  modemImei?: string;
	 simPin?: string;
  apn: string;
  proxyPort: number;
  baudRate: number;
  dataBits: number;
  stopBits: number;
  parity: string;
  deviceBackend: "at" | "qmi" | "pcsc";
  esimTransport: "at" | "qmi" | "pcsc" | "none";
  qmiUseProxy: boolean;
  qmiProxyPath?: string;
  qmiProxyExecutable?: string;
  smsEnabled: boolean;
  vowifiEnabled: boolean;
}

export interface USBNetMode {
  mode: number;
  name: string;
  rebootRequired?: boolean;
}

export interface OperatorSelection {
  mode: number;
  format: number;
  operator: string;
  accessTechnology?: string;
}

export interface CardPolicy {
  iccid: string;
  vowifiEnabled: boolean;
  airplaneEnabled: boolean;
  apn?: string;
  ipVersion?: string;
  customPhoneNumber?: string;
  source?: string;
  createdAt?: string;
  updatedAt?: string;
}

export interface SMSContact {
  deviceId: string;
  deviceName?: string;
  modemImei?: string;
  iccid?: string;
  imsi: string;
  localPhone?: string;
  peer: string;
  displayName?: string;
  lastMessage?: string;
  lastContent?: string;
  lastTimestamp: string;
  direction?: string;
  lastType?: string;
  lastSmsId?: number;
  unreadCount: number;
  messageCount?: number;
}

export interface SMSMessage {
  id: number;
  messageId?: string;
  deviceId: string;
  modemImei?: string;
  iccid?: string;
  imsi: string;
  peer: string;
  direction: "inbound" | "outbound" | "received" | "sent";
  body?: string;
  content?: string;
  sender?: string;
  recipient?: string;
  localPhone?: string;
  deviceName?: string;
  type?: string;
  timestamp: string;
  status: string;
  source?: string;
  deliveryState?: string;
  // Service-centre address (SCA) and the two clocks a received message
  // carries: when the service centre handled it, and when this server did.
  serviceCenter?: string;
  serviceCenterTimestamp?: string | null;
  receivedAt?: string;
}

export interface UpstreamProxy {
  id: string;
  name: string;
  addr: string;
  username: string;
  password?: string;
  enabled: boolean;
}

export interface UpstreamProxyProbe {
  reachable?: boolean;
  handshakeOk?: boolean;
  udpAssociateOk?: boolean;
  udpExchangeOk?: boolean;
  authMethod?: string;
  relayAddr?: string;
  dnsServer?: string;
  dnsName?: string;
  dnsRcode?: number;
  roundTripMs?: number;
  diagnosis?: string;
  hint?: string;
  error?: string;
}

export interface UpstreamProxySaveResponse {
  status?: string;
  proxy?: UpstreamProxy;
  probe?: UpstreamProxyProbe;
  message?: string;
}

export interface Country {
  countryCode: string;
  countryName: string;
  mccs: string[];
}

export interface CountryRule {
  countryCode: string;
  countryName?: string;
  upstreamProxyId: string;
  enabled: boolean;
}

export interface DeviceProxyBinding {
  deviceId: string;
  iccid: string;
  profileName: string;
  upstreamProxyId: string;
  reconnectRequested?: boolean;
  reconnectError?: string;
}

export interface ProfileProxyCandidate {
  deviceId: string;
  iccid: string;
  profileName: string;
  stateText?: string;
}

export interface LogEntry {
  time: string;
  level: "debug" | "info" | "warn" | "error" | string;
  message: string;
  caller?: string;
  fields?: string | Record<string, unknown>;
}

export interface EsimProfile {
  iccid: string;
  name: string;
  serviceProviderName: string;
  state: number;
  stateText: string;
  classText?: string;
}

export interface EsimEuiccProfiles {
  eid: string;
  aidHex: string;
  profiles: EsimProfile[];
}

export interface EsimOverview {
  available?: boolean;
  reason?: string;
  chipInfo?: {
    serialNumber?: string;
    skuName?: string;
    firmware?: string;
    eids?: Array<Record<string, unknown>>;
  };
  profiles: EsimEuiccProfiles[];
}

export interface NotificationSettings {
  telegram: Record<string, unknown>;
  webhook: Record<string, unknown>;
  bark: Record<string, unknown>;
  email: Record<string, unknown>;
  pushplus: Record<string, unknown>;
  wecom: Record<string, unknown>;
  lark: Record<string, unknown>;
}

// 网络访问控制策略：默认仅放行内网网段，可切换到对公网开放。
export interface SecuritySettings {
  mode: "internal" | "public";
  allowedCidrs: string[];
  trustProxyHeaders: boolean;
  clientIp: string;
  clientAllowed: boolean;
}

// 运行日志保留策略：全局硬上限 10000 条，可配置更严格的条数或天数限制。
export interface LoggingSettings {
  mode: "unlimited" | "count" | "days";
  count: number;
  days: number;
  storedLogs: number;
  maxLogs: number;
}

export interface SystemInfo {
  version: string;
  buildTime: string;
  config: string;
  os?: string;
  architecture?: string;
  uptime?: string;
  developer?: boolean;
}

export interface HTTPSSettings {
  enabled: boolean;
  httpUrl: string;
  httpsUrl: string;
  fingerprint?: string;
  notAfter?: string;
}

export interface DeveloperSettings {
  deviceLimit: number;
  defaultDeviceLimit: number;
  maxDeviceLimit: number;
  smsHourlyLimit: number;
  defaultSmsHourlyLimit: number;
  maxSmsHourlyLimit: number;
  autoClearModemStorage?: boolean;
}

export type Notice = {
  kind: "success" | "error" | "warning" | "info";
  title: string;
  detail?: string;
} | null;

export interface SMSTestEndpoint {
  id: string;
  name: string;
  method: string;
  url: string;
  username: string;
  hasPassword: boolean;
  headers: string;
  bodyParams: string;
  createdAt: string;
  updatedAt: string;
}

export interface SMSTestSchedule {
  id: string;
  name: string;
  endpointId: string;
  recipient: string;
  sender: string;
  contentTemplate: string;
  codeType: string;
  codeLength: number;
  frequencyMinutes: number;
  startTime: string;
  enabled: boolean;
  isExternal: boolean;
  lastRunAt: string | null;
  createdAt: string;
  updatedAt: string;
}

export interface SMSTestResult {
  id: number;
  scheduleId: string;
  sentAt: string;
  receivedAt: string | null;
  elapsedMs: number | null;
  code: string;
  status: string;
  sendResponse: string;
  deviceId: string;
}

export interface SMSTestResultsResponse {
  results: SMSTestResult[];
  summary: Record<string, number>;
  hours: number;
}

// Call mirrors vowifi.Call from the IMS provider. `codec` and `mediaReady`
// only appear once SDP has been negotiated — on a provisional response with a
// body (early media) or on the 200 OK — so they are absent while dialing.
export interface Call {
  id: string;
  number: string;
  direction: string;
  state: string;
  startedAt: string;
  answeredAt?: string;
  sipCode?: number;
  reason?: string;
  mediaReady?: boolean;
  codec?: string;
  endedAt?: string;
}

export interface CallsResponse {
  deviceId: string;
  // "vowifi" once IMS registration completes, otherwise "cellular" (AT+CLCC).
  transport: string;
  calls: Call[];
}

// One completed or in-progress call. Written by observing live call state, so
// a call placed from the browser, through the SIP trunk or by an automatic
// task all appear the same way.
export type CallRecord = {
  id: number;
  callId: string;
  deviceId: string;
  deviceName?: string;
  direction?: string;
  // source is "browser" or "trunk": who drove the call.
  source?: string;
  peerNumber?: string;
  startedAt: string;
  answeredAt?: string;
  endedAt?: string;
  // durationSeconds counts from the answer, so a call that only rang is zero.
  durationSeconds: number;
  disposition?: string;
  sipCode?: number;
  reason?: string;
};

export type CallRecordsResponse = {
  records: CallRecord[];
  total: number;
  limit: number;
  offset: number;
};

// --- Asterisk (AsteriskPage) ---

// One raw AMI field, kept as a name/value pair so the API client's camelCase
// transform cannot rewrite the wire names this exists to show.
export type AsteriskField = { name: string; value: string };

export type AsteriskContact = {
  uri?: string;
  status?: string;
  roundtripMs?: number;
  expires?: string;
  userAgent?: string;
  viaAddress?: string;
  fields?: AsteriskField[];
};

export type AsteriskEndpoint = {
  name: string;
  aor?: string;
  state?: string;
  activeChannels?: string;
  transport?: string;
  contacts: AsteriskContact[];
  registered: boolean;
  reachable?: boolean;
  fields?: AsteriskField[];
};

// One live channel: a call leg Asterisk is carrying right now.
export type AsteriskChannel = {
  name: string;
  state?: string;
  callerId?: string;
  connectedLine?: string;
  context?: string;
  extension?: string;
  application?: string;
  duration?: string;
  bridgeId?: string;
  uniqueId?: string;
  fields?: AsteriskField[];
};

export type AsteriskStatus = {
  // configured false means no manager address is set at all, which is the
  // normal state for a VoCat with no PBX in front of it -- not an error.
  configured: boolean;
  reachable?: boolean;
  address?: string;
  version?: string;
  error?: string;
  contactsError?: string;
  // Contacts Asterisk returned that matched no endpoint. Normally absent;
  // present means a pairing gap, which is worth seeing rather than hiding.
  unpairedContacts?: AsteriskContact[];
  core?: { startupTime?: string; reloadTime?: string; calls?: string };
  endpoints: AsteriskEndpoint[];
  channels?: AsteriskChannel[];
  channelsError?: string;
};

// One change in an endpoint contact's state. Recorded whether or not anyone
// is looking, which is the point: a handset that drops for ninety seconds an
// hour is invisible to a page you have to be watching.
export type AsteriskRegistration = {
  endpoint: string;
  contactUri?: string;
  status?: string;
  previousStatus?: string;
  userAgent?: string;
  viaAddress?: string;
  roundtripMs?: number;
  changedAt: string;
};

export type AsteriskRoute = {
  pattern: string;
  devices: string[];
  timeoutSeconds: number;
  comment?: string;
};

// A softphone account. The password is write-only: it is sent on save and
// never returned, so the browser only ever learns whether one is set.
export type AsteriskExtension = {
  name: string;
  password?: string;
  callerId?: string;
  maxContacts: number;
  comment?: string;
  hasPassword?: boolean;
};

// Where a call arriving on a SIM rings.
export type AsteriskInbound = {
  // "did" rings the extension named after the dialled number, "ring_all"
  // rings everything at once, "hunt" rings the list in order, "forward"
  // sends the call straight back out to a trunk without ringing anything.
  mode: "did" | "ring_all" | "hunt" | "forward";
  // In hunt mode this is the order and is required. In ring-all an empty list
  // means every configured extension, so a new handset joins without a second
  // edit.
  extensions?: string[];
  ringSeconds: number;
  huntSeconds: number;
  // Forward mode only. An empty forwardNumber passes the dialled number
  // through, which is what a provider routing by DID expects.
  forwardTrunk?: string;
  forwardNumber?: string;
};

// One SIM number VoCat has learned, offered as an extension to create.
export type AsteriskExtensionCandidate = {
  number: string;
  deviceId?: string;
  deviceName?: string;
  iccid?: string;
  exists: boolean;
};

export type AsteriskExtensions = {
  extensions: AsteriskExtension[];
  // preview is the PJSIP configuration these render to, with the password
  // lines masked.
  preview?: string;
  // internalPreview is the extension-to-extension dialplan generated from the
  // same list. No secret in it, so it is shown whole.
  internalPreview?: string;
  // inboundPreview is where a call arriving on a SIM rings.
  inboundPreview?: string;
  inbound?: AsteriskInbound;
  pending?: boolean;
  path?: string;
  canApply?: boolean;
  // seeded means the file Asterisk is using came from the container
  // entrypoint rather than from VoCat -- the account in .env, which saving
  // here replaces.
  seeded?: boolean;
  minPasswordLength?: number;
  error?: string;
};

// An external SIP peer whose calls are relayed out through a SIM. The
// password is write-only: it is sent on save and never returned, so the
// browser only ever learns whether one is set.
export type AsteriskTrunk = {
  name: string;
  host: string;
  port: number;
  transport: string;
  // Addresses or CIDR prefixes to identify the peer by. Either this or
  // credentials is required, or anything reaching the port could dial out.
  match?: string[];
  username?: string;
  password?: string;
  hasPassword?: boolean;
  // The outbound pair answers a challenge to an INVITE this side sends, which
  // is what a provider does when a call is forwarded out to it. Separate from
  // the inbound pair: a provider issuing one credential for both is served by
  // entering it twice, one issuing two cannot be served by a single field.
  outboundUsername?: string;
  outboundPassword?: string;
  hasOutboundPassword?: boolean;
  // Patterns this trunk may dial. Empty means none: a new trunk reaches
  // nothing until destinations are added.
  destinations?: string[];
  devices: string[];
  maxConcurrent: number;
  timeoutSeconds: number;
  // shareRoutes lets the trunk reach the shared outbound routes as well. Off
  // by default: those calls also escape this trunk's SIM list and cap.
  shareRoutes?: boolean;
  comment?: string;
};

export type AsteriskTrunks = {
  trunks: AsteriskTrunk[];
  preview?: string;
  routesPreview?: string;
  pending?: boolean;
  path?: string;
  writable?: boolean;
  canApply?: boolean;
  unknownDevices?: string[];
  error?: string;
};

export type AsteriskRoutes = {
  routes: AsteriskRoute[];
  // preview is the dialplan these routes render to, so a syntax question can
  // be answered without shelling into the container.
  preview?: string;
  // pending means the file on disk no longer matches the stored routes, which
  // is exactly what Apply resolves.
  pending?: boolean;
  path?: string;
  writable?: boolean;
  canApply?: boolean;
  // Device names a route uses that match no configured device. The dialplan
  // is still valid and Apply still succeeds, so this is the only place the
  // mistake is visible before a caller hears it.
  unknownDevices?: string[];
  error?: string;
};
