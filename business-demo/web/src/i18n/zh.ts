import { TranslationKey } from './en';

export const zh: Record<TranslationKey, string> = {
  // 品牌与顶部栏
  brand_title: 'WMS 打包工作台控制台',
  brand_sub: '多卷纸规格智能分流对接参考范例',
  nav_orders: '包装工作台',
  nav_inspector: '原生 API 报文透视',
  connected: '已连接',
  connecting: '连接中…',
  refresh: '刷新',
  auto_refresh: '自动刷新',
  lang_switch: '语言 / Language',
  theme_light: '切换至浅色模式',
  theme_dark: '切换至深色模式',

  // API 报文调试与抓包
  inspector_title: '原生 HTTP API 报文抓包与回放',
  inspector_subtitle: '实时捕获 business-demo 与云打印服务之间的全量底层 HTTP 请求与响应报文，提供完整 cURL 与报文副本。',
  filter_all: '全量调用',
  filter_dispatch: '任务分发',
  filter_fleet: '打印机状态',
  filter_preview: '模板预览',
  clear_history: '清空调用历史',
  no_traces: '暂无捕获的 API 调用流水。可尝试释放波次或点击上方快速触发测试请求。',
  select_trace_prompt: '从左侧列表中选择一项 API 请求以查看底层完整的 HTTP 头部、请求参数与响应内容。',
  quick_triggers: '即时调试触发器：',
  trigger_fleet: '拉取群状态 (GET /printers/status)',
  trigger_job: '下发 4x6 任务 (POST /jobs)',
  trigger_preview: '光栅化预览 (POST /admin/preview)',
  copy_curl: '复制为 cURL',
  copy_req: '复制请求体',
  copy_res: '复制响应体',
  copied_curl: '已复制 cURL 调试命令！',
  copied_req: '已复制请求参数体！',
  copied_res: '已复制响应数据体！',
  section_request: 'HTTP 客户端请求报文 (Request)',
  section_response: 'HTTP 服务端响应报文 (Response)',
  request_headers: '请求头 (Headers)',
  response_headers: '响应头 (Headers)',
  request_body: '请求体 (Body)',
  response_body: '响应体 (Body)',

  // 卷纸规格横幅
  strip_label: '已装载卷纸打印机列表',
  strip_sub: '现场打印机已装载固定尺寸的热敏标签卷，不可动态混合换纸',
  no_printers_registered: '服务端暂无注册的打印机设备。',
  usable: '就绪可用',
  offline: '离线/不可用',

  // 订单发运区域
  orders_title: '出库包装批次 (多规格标签混排波次)',
  orders_subtitle: '订单明细行各对应不同规格文档，系统根据所需规格自动路由至对应装载该卷纸的打印机。',
  no_orders: '暂无待出库订单。',
  release_wave: '释放波次',
  resend_wave: '重发波次 (幂等验证)',
  releasing: '波次调度中…',
  resending: '幂等校验中…',
  replayed: '幂等去重',
  attempts: '尝试 {count} 次',

  // 订单行
  roll_printer_label: '卷纸打印机：',
  no_compatible_printer: '暂无可用的打印机',
  size_mismatch: '卷纸不匹配',
  size_mismatch_title: '当前选择的打印机装载的卷纸与该文档所需尺寸不一致！',
  preview_btn: '标签预览',
  not_released: '未释放',

  // 状态
  state_queued: '排队中',
  state_sending: '传输中',
  state_printed: '已打印 (已确认)',
  state_sent_unknown: '状态未明',
  state_failed: '失败',
  state_rejected: '已拒绝',
  state_error: '调用异常',

  // 侧边栏：架构说明
  arch_title: '架构实践：卷纸规格自动路由',
  arch_desc: '工业标签打印机装载特定尺寸纸卷后无法在任务间动态更换。在标准仓储自动化包装场景中，各包装工位需根据文档规格分流至匹配设备：',
  roll_4x6_title: '托盘栈板 / 出库大物流标',
  roll_4x6_desc: '101.6 × 152.4 mm · 承运商全幅面标准发运单',
  roll_4x2_title: '外箱纸箱 / 周转箱标',
  roll_4x2_desc: '101.6 × 50.8 mm · 打包作业箱条码与内容清单',
  roll_2x1_title: '单件 SKU / 拣选小件标',
  roll_2x1_desc: '50.8 × 25.4 mm · 单品物料条码与货位指引',
  arch_idempotency_rule: '幂等法则：重新释放相同订单时，每个明细行严格复用相同的 external_id 幂等键，确保底层物理设备绝对零重打！',

  // 侧边栏：预览
  preview_title: '标签光栅化即时预览',
  preview_rendering: '正在根据版面规格实时渲染…',
  preview_empty_prompt: '点击任意明细行的“标签预览”按钮，即可按其真实物理宽高比例查看渲染效果。',
  preview_error: '渲染失败: {error}',

  // 侧边栏：事件流
  events_title: '实时调度流水日志',
  events_ready: 'WMS 控制台已就绪连接',
  wave_released_log: '波次已释放 {order}: {lines} 行标签已分发至对应卷纸打印机',
  wave_resend_log: '波次重发 {order}: {lines} 行已重新提交 ({replayed} 行成功触发幂等去重)',
  rerouted_log: '行 L{line} 已重新路由至 {printer}',
  rendered_preview_log: '已渲染 {order} 行 L{line} 标签预览 ({size})',
};
