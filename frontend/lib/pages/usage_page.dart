import 'dart:async';

import 'package:flutter/material.dart';

import '../api_client.dart';
import '../models.dart';
import '../theme.dart';
import '../ui/feedback.dart';
import '../ui/page_header.dart';

/// 用量统计页：四桶 token（新增输入/输出/缓存创建/缓存命中）的总指标、
/// 按小时或按天的趋势、以及请求日志/账号/模型三个维度的明细。
/// 口径与 CC Switch 使用统计对齐：输入为扣缓存后的净输入，
/// 真实消耗 = 净输入 + 输出 + 缓存创建 + 缓存命中。
class UsagePage extends StatefulWidget {
  const UsagePage({super.key, required this.client, this.onOpenSettings});

  final ApiClient client;
  final VoidCallback? onOpenSettings;

  @override
  State<UsagePage> createState() => _UsagePageState();
}

enum _Range { today, d7, d30, all }

const _rangeLabels = {
  _Range.today: '当天',
  _Range.d7: '近 7 天',
  _Range.d30: '近 30 天',
  _Range.all: '全部',
};

/// 自动刷新间隔（秒），0 为关；与 CC Switch 的 0/5/10/30/60s 同档。
const _refreshOptions = [0, 5, 10, 30, 60];

class _UsagePageState extends State<UsagePage> {
  _Range _range = _Range.today;
  String _granularity = 'auto'; // auto | hour | day
  int _tab = 0; // 0 请求日志 1 账号统计 2 模型统计

  /// 页头过滤器：来源（转发/对话）与模型，空串表示「全部」。
  String _source = '';
  String _model = '';
  List<String> _modelOptions = const [];

  /// 自动刷新间隔秒数；改档即重建定时器。
  int _refreshSec = 30;
  Timer? _timer;

  bool _busy = false;
  Object? _error;

  UsageTotals? _summary;
  String _trendGran = 'hour';
  List<UsageBucket> _buckets = const [];
  List<UsageGroup> _groups = const [];
  List<UsageLogRow> _logs = const [];
  int _logTotal = 0;

  static const _pageSize = 50;

  @override
  void initState() {
    super.initState();
    _loadModelOptions();
    _load();
    _restartTimer();
  }

  @override
  void dispose() {
    _timer?.cancel();
    super.dispose();
  }

  /// 模型下拉的选项：配置里的命名模型清单。失败不阻断页面。
  Future<void> _loadModelOptions() async {
    try {
      final models = await widget.client.listModels();
      if (!mounted) return;
      setState(() => _modelOptions = [for (final m in models) m.id]);
    } catch (_) {
      // 过滤器选项拉不到时保持只有「全部模型」，不打断主数据加载。
    }
  }

  void _restartTimer() {
    _timer?.cancel();
    _timer = null;
    if (_refreshSec <= 0) return;
    _timer = Timer.periodic(Duration(seconds: _refreshSec), (_) => _load());
  }

  void _setRefresh(int sec) {
    if (sec == _refreshSec) return;
    setState(() => _refreshSec = sec);
    _restartTimer();
  }

  /// 当前区间的时间边界。all 返回 (null, null) 表示不限。
  (DateTime?, DateTime?) get _bounds {
    final now = DateTime.now();
    switch (_range) {
      case _Range.today:
        return (DateTime(now.year, now.month, now.day), now);
      case _Range.d7:
        return (now.subtract(const Duration(days: 7)), now);
      case _Range.d30:
        return (now.subtract(const Duration(days: 30)), now);
      case _Range.all:
        return (null, null);
    }
  }

  Future<void> _load({bool appendLogs = false}) async {
    setState(() {
      _busy = true;
      _error = null;
    });
    final (start, end) = _bounds;
    try {
      if (!appendLogs) {
        final results = await Future.wait<Object>([
          widget.client.usageSummary(
              start: start, end: end, model: _modelOr(null), source: _sourceOr(null)),
          widget.client.usageTrend(
              start: start,
              end: end,
              model: _modelOr(null),
              source: _sourceOr(null),
              granularity: _granularity == 'auto' ? null : _granularity),
          _tabData(start, end, _tab),
        ]);
        if (!mounted) return;
        final trend = results[1] as (String, List<UsageBucket>);
        setState(() {
          _summary = results[0] as UsageTotals;
          _trendGran = trend.$1;
          _buckets = trend.$2;
          _applyTabData(results[2], reset: true);
        });
      } else {
        final data = await _tabData(start, end, 0, offset: _logs.length);
        if (!mounted) return;
        setState(() => _applyTabData(data, reset: false));
      }
    } catch (e) {
      if (!mounted) return;
      setState(() => _error = e);
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  String? _modelOr(String? fallback) => _model.isEmpty ? fallback : _model;
  String? _sourceOr(String? fallback) => _source.isEmpty ? fallback : _source;

  /// 当前页签的数据拉取：0 日志 / 1 账号 / 2 模型。过滤器按各端点支持的维度下发。
  Future<Object> _tabData(DateTime? start, DateTime? end, int tab, {int offset = 0}) {
    switch (tab) {
      case 1:
        return widget.client.usageAccounts(
            start: start, end: end, model: _modelOr(null), source: _sourceOr(null));
      case 2:
        return widget.client.usageModels(start: start, end: end, source: _sourceOr(null));
      default:
        return widget.client.usageLogs(
            start: start,
            end: end,
            model: _modelOr(null),
            source: _sourceOr(null),
            limit: _pageSize,
            offset: offset);
    }
  }

  void _applyTabData(Object data, {required bool reset}) {
    switch (data) {
      case List<UsageGroup> g:
        _groups = g;
      case (List<UsageLogRow> rows, int total):
        _logs = reset ? rows : [..._logs, ...rows];
        _logTotal = total;
    }
  }

  Future<void> _switchTab(int tab) async {
    if (tab == _tab) return;
    setState(() {
      _tab = tab;
      _groups = const [];
      _logs = const [];
      _logTotal = 0;
    });
    final (start, end) = _bounds;
    setState(() => _busy = true);
    try {
      final data = await _tabData(start, end, tab);
      if (!mounted) return;
      setState(() => _applyTabData(data, reset: true));
    } catch (e) {
      if (!mounted) return;
      setState(() => _error = e);
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        PageHeader(
          title: '用量',
          // 页头右侧过滤器,仿 CC Switch 使用统计:来源/模型/自动刷新/区间。
          trailing: [
            _filterSelect(
              t,
              label: _source.isEmpty ? '全部来源' : (_source == 'chat' ? '对话' : '转发'),
              items: const [
                ('', '全部来源'),
                ('proxy', '转发'),
                ('chat', '对话'),
              ],
              value: _source,
              onSelected: (v) {
                if (v == _source) return;
                setState(() => _source = v);
                _load();
              },
            ),
            const SizedBox(width: 8),
            _filterSelect(
              t,
              label: _model.isEmpty ? '全部模型' : _model,
              width: 132,
              items: [
                const ('', '全部模型'),
                for (final m in _modelOptions) (m, m),
              ],
              value: _model,
              onSelected: (v) {
                if (v == _model) return;
                setState(() => _model = v);
                _load();
              },
            ),
            const SizedBox(width: 8),
            _filterSelect(
              t,
              icon: Icons.refresh,
              label: _refreshSec <= 0 ? '关闭' : '${_refreshSec}s',
              items: [
                for (final s in _refreshOptions)
                  (s.toString(), s <= 0 ? '关闭' : '${s}s'),
              ],
              value: '$_refreshSec',
              onSelected: (v) => _setRefresh(int.parse(v)),
            ),
            const SizedBox(width: 8),
            _filterSelect(
              t,
              icon: Icons.calendar_month_outlined,
              label: _rangeLabels[_range]!,
              items: [
                for (final r in _Range.values) (r.name, _rangeLabels[r]!),
              ],
              value: _range.name,
              onSelected: (v) {
                final r = _Range.values.firstWhere((e) => e.name == v);
                if (r == _range) return;
                setState(() => _range = r);
                _load();
              },
            ),
          ],
        ),
        Expanded(
          child: SingleChildScrollView(
            padding: const EdgeInsets.fromLTRB(24, 0, 24, 24),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                if (_error != null)
                  Padding(
                    padding: const EdgeInsets.only(bottom: 16),
                    child: SelectableText(
                      describeError(_error!),
                      style: TextStyle(fontSize: 12.5, color: t.danger),
                    ),
                  ),
                _hero(t),
                const SizedBox(height: 14),
                _statCards(t),
                const SizedBox(height: 14),
                _trendCard(t),
                const SizedBox(height: 14),
                _tabBar(t),
                const SizedBox(height: 14),
                _tabContent(t),
              ],
            ),
          ),
        ),
      ],
    );
  }

  /// 页头下拉过滤器：圆角描边触发器 + 弹出菜单，形态对齐 CC Switch 的 Select。
  Widget _filterSelect(
    AppTokens t, {
    required String label,
    required List<(String, String)> items,
    required String value,
    required ValueChanged<String> onSelected,
    IconData? icon,
    double width = 104,
  }) {
    return PopupMenuButton<String>(
      tooltip: '',
      padding: EdgeInsets.zero,
      color: t.surface,
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(10)),
      elevation: 3,
      offset: const Offset(0, 4),
      onSelected: onSelected,
      itemBuilder: (context) => [
        for (final (v, text) in items)
          PopupMenuItem<String>(
            value: v,
            height: 34,
            child: Row(
              children: [
                Expanded(
                  child: Text(
                    text,
                    overflow: TextOverflow.ellipsis,
                    style: TextStyle(
                      fontSize: 12.5,
                      fontWeight: v == value ? FontWeight.w600 : FontWeight.w400,
                      color: v == value ? t.primaryInk : t.ink,
                    ),
                  ),
                ),
                if (v == value) Icon(Icons.check, size: 14, color: t.primary),
              ],
            ),
          ),
      ],
      child: Container(
        width: width,
        height: 34,
        padding: const EdgeInsets.symmetric(horizontal: 10),
        decoration: BoxDecoration(
          color: t.surface,
          border: Border.all(color: t.border),
          borderRadius: BorderRadius.circular(8),
        ),
        child: Row(
          children: [
            if (icon != null) ...[
              Icon(icon, size: 14, color: t.dim),
              const SizedBox(width: 6),
            ],
            Expanded(
              child: Text(
                label,
                overflow: TextOverflow.ellipsis,
                style: TextStyle(fontSize: 12.5, color: t.ink),
              ),
            ),
            Icon(Icons.expand_more, size: 16, color: t.faint),
          ],
        ),
      ),
    );
  }

  Widget _chip(AppTokens t, String label, bool on, VoidCallback onTap) {
    return InkWell(
      borderRadius: BorderRadius.circular(8),
      onTap: onTap,
      child: Container(
        padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 6),
        decoration: BoxDecoration(
          color: on ? t.primarySoft : Colors.transparent,
          border: Border.all(color: on ? t.primary : t.border),
          borderRadius: BorderRadius.circular(8),
        ),
        child: Text(
          label,
          style: TextStyle(
            fontSize: 12.5,
            fontWeight: on ? FontWeight.w600 : FontWeight.w500,
            color: on ? t.primaryInk : t.dim,
          ),
        ),
      ),
    );
  }

  /// 顶部大卡：真实消耗 + 请求数/成功率。
  Widget _hero(AppTokens t) {
    final s = _summary;
    return Container(
      padding: const EdgeInsets.all(20),
      decoration: BoxDecoration(
        color: t.surface,
        border: Border.all(color: t.border),
        borderRadius: BorderRadius.circular(12),
      ),
      child: Row(
        children: [
          Container(
            width: 40,
            height: 40,
            decoration: BoxDecoration(
              color: t.primarySoft,
              borderRadius: BorderRadius.circular(20),
            ),
            child: Icon(Icons.bolt_outlined, size: 20, color: t.primaryInk),
          ),
          const SizedBox(width: 14),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text('真实消耗 Tokens',
                    style: TextStyle(fontSize: 12.5, color: t.faint)),
                const SizedBox(height: 2),
                Text(
                  s == null ? '—' : _fmtTokens(s.realTotal),
                  style: TextStyle(
                      fontSize: 26, fontWeight: FontWeight.w700, color: t.ink),
                ),
              ],
            ),
          ),
          _heroMetric(t, '总请求数', s == null ? '—' : '${s.requests}'),
          const SizedBox(width: 28),
          _heroMetric(
              t,
              '成功率',
              s == null || s.requests == 0
                  ? '—'
                  : '${(s.success / s.requests * 100).toStringAsFixed(1)}%'),
        ],
      ),
    );
  }

  Widget _heroMetric(AppTokens t, String label, String value) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.end,
      children: [
        Text(label, style: TextStyle(fontSize: 11.5, color: t.faint)),
        const SizedBox(height: 2),
        Text(value,
            style: TextStyle(fontSize: 16, fontWeight: FontWeight.w600, color: t.ink)),
      ],
    );
  }

  /// 四桶 + 命中率卡片行。
  Widget _statCards(AppTokens t) {
    final s = _summary;
    return LayoutBuilder(builder: (context, c) {
      final wide = c.maxWidth >= 900;
      final cards = [
        _statCard(t, '新增输入', Icons.arrow_downward, t.primary,
            s == null ? '—' : _fmtTokens(s.input)),
        _statCard(t, '输出', Icons.arrow_upward, t.success,
            s == null ? '—' : _fmtTokens(s.output)),
        _statCard(t, '缓存创建', Icons.storage_outlined, t.warn,
            s == null ? '—' : _fmtTokens(s.cacheWrite)),
        _statCard(t, '缓存命中', Icons.flash_on_outlined, t.violet,
            s == null ? '—' : _fmtTokens(s.cacheRead)),
        _hitRateCard(t, s),
      ];
      if (wide) {
        return Row(children: [
          for (var i = 0; i < cards.length; i++) ...[
            if (i > 0) const SizedBox(width: 14),
            Expanded(child: cards[i]),
          ],
        ]);
      }
      return Wrap(
        spacing: 14,
        runSpacing: 14,
        children: [for (final c2 in cards) SizedBox(width: 220, child: c2)],
      );
    });
  }

  Widget _statCard(AppTokens t, String label, IconData icon, Color color, String value) {
    return Container(
      padding: const EdgeInsets.all(16),
      decoration: BoxDecoration(
        color: t.surface,
        border: Border.all(color: t.border),
        borderRadius: BorderRadius.circular(12),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Icon(icon, size: 14, color: color),
              const SizedBox(width: 6),
              Text(label, style: TextStyle(fontSize: 12, color: t.faint)),
            ],
          ),
          const SizedBox(height: 8),
          Text(value,
              style: TextStyle(fontSize: 18, fontWeight: FontWeight.w600, color: t.ink)),
        ],
      ),
    );
  }

  Widget _hitRateCard(AppTokens t, UsageTotals? s) {
    final rate = s?.hitRate ?? 0;
    return Container(
      padding: const EdgeInsets.all(16),
      decoration: BoxDecoration(
        color: t.surface,
        border: Border.all(color: t.border),
        borderRadius: BorderRadius.circular(12),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            mainAxisAlignment: MainAxisAlignment.spaceBetween,
            children: [
              Text('缓存命中率', style: TextStyle(fontSize: 12, color: t.faint)),
              Text('${(rate * 100).toStringAsFixed(1)}%',
                  style: TextStyle(
                      fontSize: 12.5, fontWeight: FontWeight.w600, color: t.success)),
            ],
          ),
          const SizedBox(height: 12),
          ClipRRect(
            borderRadius: BorderRadius.circular(3),
            child: LinearProgressIndicator(
              value: rate.clamp(0, 1),
              minHeight: 6,
              backgroundColor: t.border,
              valueColor: AlwaysStoppedAnimation(t.violet),
            ),
          ),
        ],
      ),
    );
  }

  /// 趋势卡：四条折线（缓存创建/缓存命中/输入/输出）。
  Widget _trendCard(AppTokens t) {
    return Container(
      padding: const EdgeInsets.all(20),
      decoration: BoxDecoration(
        color: t.surface,
        border: Border.all(color: t.border),
        borderRadius: BorderRadius.circular(12),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Text('使用趋势',
                  style: TextStyle(fontSize: 14, fontWeight: FontWeight.w600, color: t.ink)),
              const Spacer(),
              // 粒度只影响趋势分桶，放在趋势卡头部而非页头过滤器。
              for (final g in const [
                ['auto', '自动'],
                ['hour', '按小时'],
                ['day', '按天'],
              ])
                Padding(
                  padding: const EdgeInsets.only(left: 6),
                  child: _chip(t, g[1], _granularity == g[0], () {
                    setState(() => _granularity = g[0]);
                    _load();
                  }),
                ),
            ],
          ),
          const SizedBox(height: 18),
          SizedBox(
            height: 220,
            child: _TrendChart(buckets: _buckets, granularity: _trendGran),
          ),
          const SizedBox(height: 10),
          Row(
            mainAxisAlignment: MainAxisAlignment.center,
            children: [
              _legend(t, '缓存创建', t.warn),
              _legend(t, '缓存命中', t.violet),
              _legend(t, '输入', t.primary),
              _legend(t, '输出', t.success),
            ],
          ),
        ],
      ),
    );
  }

  Widget _legend(AppTokens t, String label, Color color) {
    return Padding(
      padding: const EdgeInsets.symmetric(horizontal: 10),
      child: Row(
        children: [
          Container(width: 8, height: 8, decoration: BoxDecoration(color: color, shape: BoxShape.circle)),
          const SizedBox(width: 5),
          Text(label, style: TextStyle(fontSize: 11.5, color: t.dim)),
        ],
      ),
    );
  }

  Widget _tabBar(AppTokens t) {
    const labels = ['请求日志', '账号统计', '模型统计'];
    return Row(
      children: [
        for (var i = 0; i < labels.length; i++)
          Padding(
            padding: const EdgeInsets.only(right: 8),
            child: _chip(t, labels[i], _tab == i, () => _switchTab(i)),
          ),
      ],
    );
  }

  Widget _tabContent(AppTokens t) {
    switch (_tab) {
      case 1:
        return _groupTable(t, _groups, '账号');
      case 2:
        return _groupTable(t, _groups, '模型');
      default:
        return _logTable(t);
    }
  }

  /// 账号/模型聚合表。
  Widget _groupTable(AppTokens t, List<UsageGroup> groups, String keyLabel) {
    return _tableCard(t, [
      _tableHeader(t, [
        (keyLabel, 2.2),
        ('请求', 1),
        ('新增输入', 1.2),
        ('输出', 1.2),
        ('缓存创建', 1.2),
        ('缓存命中', 1.2),
        ('命中率', 1),
      ]),
      if (groups.isEmpty) _emptyRow(t, '该区间暂无数据'),
      for (final g in groups)
        _tableRow(t, [
          _cell(t, g.key, mono: true, flex: 2.2),
          _cell(t, '${g.totals.requests}', flex: 1),
          _cell(t, _fmtTokens(g.totals.input), flex: 1.2),
          _cell(t, _fmtTokens(g.totals.output), flex: 1.2),
          _cell(t, _fmtTokens(g.totals.cacheWrite), flex: 1.2),
          _cell(t, _fmtTokens(g.totals.cacheRead), flex: 1.2),
          _cell(t, '${(g.totals.hitRate * 100).toStringAsFixed(1)}%', flex: 1),
        ]),
    ]);
  }

  /// 请求日志表 + 加载更多。
  Widget _logTable(AppTokens t) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        _tableCard(t, [
          _tableHeader(t, [
            ('时间', 1.6),
            ('来源', 0.8),
            ('账号', 1.4),
            ('模型', 1.8),
            ('输入', 1),
            ('输出', 1),
            ('缓存创建', 1),
            ('缓存命中', 1),
            ('用时', 0.9),
            ('状态', 0.8),
          ]),
          if (_logs.isEmpty) _emptyRow(t, '该区间暂无请求'),
          for (final l in _logs)
            _tableRow(t, [
              _cell(t, _fmtTime(l.createdAt), flex: 1.6),
              _cell(t, l.source == 'chat' ? '对话' : '转发', flex: 0.8),
              _cell(t, l.account, flex: 1.4),
              _cell(t, l.modelId, mono: true, flex: 1.8),
              _cell(t, _fmtTokens(l.input), flex: 1),
              _cell(t, _fmtTokens(l.output), flex: 1),
              _cell(t, _fmtTokens(l.cacheWrite), flex: 1),
              _cell(t, _fmtTokens(l.cacheRead), flex: 1),
              _cell(t, l.durationMs == null ? '—' : '${(l.durationMs! / 1000).toStringAsFixed(1)}s', flex: 0.9),
              _statusCell(t, l.statusCode, flex: 0.8),
            ]),
        ]),
        if (_logs.length < _logTotal)
          Padding(
            padding: const EdgeInsets.only(top: 12),
            child: Align(
              alignment: Alignment.centerLeft,
              child: TextButton(
                onPressed: _busy ? null : () => _load(appendLogs: true),
                child: Text('加载更多（已 ${_logs.length} / $_logTotal）'),
              ),
            ),
          ),
      ],
    );
  }

  Widget _statusCell(AppTokens t, int status, {double flex = 1}) {
    final ok = status >= 200 && status < 300;
    return Expanded(
      flex: (flex * 10).round(),
      child: Text(
        status == 0 ? '—' : '$status',
        style: TextStyle(
          fontSize: 12,
          fontWeight: FontWeight.w600,
          color: ok ? t.success : t.danger,
        ),
      ),
    );
  }

  Widget _tableCard(AppTokens t, List<Widget> rows) {
    return Container(
      decoration: BoxDecoration(
        color: t.surface,
        border: Border.all(color: t.border),
        borderRadius: BorderRadius.circular(12),
      ),
      clipBehavior: Clip.antiAlias,
      child: Column(children: rows),
    );
  }

  Widget _tableHeader(AppTokens t, List<(String, double)> cols) {
    return Container(
      color: t.bg,
      padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 10),
      child: Row(
        children: [
          for (final c in cols)
            Expanded(
              flex: (c.$2 * 10).round(),
              child: Text(c.$1,
                  style: TextStyle(fontSize: 12, fontWeight: FontWeight.w600, color: t.faint)),
            ),
        ],
      ),
    );
  }

  Widget _tableRow(AppTokens t, List<Widget> cells) {
    return Container(
      decoration: BoxDecoration(
        border: Border(top: BorderSide(color: t.border)),
      ),
      padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 11),
      child: Row(children: cells),
    );
  }

  Widget _emptyRow(AppTokens t, String msg) {
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 28),
      child: Center(child: Text(msg, style: TextStyle(fontSize: 12.5, color: t.faint))),
    );
  }

  Widget _cell(AppTokens t, String text, {double flex = 1, bool mono = false}) {
    return Expanded(
      flex: (flex * 10).round(),
      child: Text(
        text,
        overflow: TextOverflow.ellipsis,
        style: TextStyle(
          fontSize: 12,
          color: t.ink,
          fontFamily: mono ? 'monospace' : null,
        ),
      ),
    );
  }
}

/// token 数的短格式：1234 → 1.2k，1234567 → 1.2M。
String _fmtTokens(int n) {
  if (n < 1000) return '$n';
  if (n < 1000000) return '${(n / 1000).toStringAsFixed(1)}k';
  return '${(n / 1000000).toStringAsFixed(2)}M';
}

String _fmtTime(DateTime t) {
  final l = t.toLocal();
  String two(int v) => v.toString().padLeft(2, '0');
  return '${two(l.month)}/${two(l.day)} ${two(l.hour)}:${two(l.minute)}:${two(l.second)}';
}

/// 四序列折线图。纵轴按最大值自适应，横轴标签按桶数抽稀。
class _TrendChart extends StatelessWidget {
  const _TrendChart({required this.buckets, required this.granularity});

  final List<UsageBucket> buckets;
  final String granularity;

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    if (buckets.isEmpty) {
      return Center(child: Text('该区间暂无趋势数据', style: TextStyle(fontSize: 12.5, color: t.faint)));
    }
    return CustomPaint(
      painter: _TrendPainter(
        buckets: buckets,
        granularity: granularity,
        ink: t.ink,
        faint: t.faint,
        border: t.border,
        series: [
          (b) => b.totals.cacheWrite.toDouble(),
          (b) => b.totals.cacheRead.toDouble(),
          (b) => b.totals.input.toDouble(),
          (b) => b.totals.output.toDouble(),
        ],
        colors: [t.warn, t.violet, t.primary, t.success],
      ),
      size: Size.infinite,
    );
  }
}

class _TrendPainter extends CustomPainter {
  _TrendPainter({
    required this.buckets,
    required this.granularity,
    required this.ink,
    required this.faint,
    required this.border,
    required this.series,
    required this.colors,
  });

  final List<UsageBucket> buckets;
  final String granularity;
  final Color ink, faint, border;
  final List<double Function(UsageBucket)> series;
  final List<Color> colors;

  @override
  void paint(Canvas canvas, Size size) {
    const left = 46.0, right = 8.0, top = 8.0, bottom = 26.0;
    final plot = Rect.fromLTRB(left, top, size.width - right, size.height - bottom);
    if (plot.width <= 0 || plot.height <= 0) return;

    double maxV = 1;
    for (final b in buckets) {
      for (final s in series) {
        final v = s(b);
        if (v > maxV) maxV = v;
      }
    }

    // 横向网格与纵轴刻度。
    final grid = TextPainter(
      textDirection: TextDirection.ltr,
      textAlign: TextAlign.right,
    );
    for (var i = 0; i <= 4; i++) {
      final y = plot.bottom - plot.height * i / 4;
      canvas.drawLine(Offset(plot.left, y), Offset(plot.right, y),
          Paint()..color = border..strokeWidth = 1);
      grid.text = TextSpan(
        text: _fmtTokens((maxV * i / 4).round()),
        style: TextStyle(fontSize: 10, color: faint),
      );
      grid.layout();
      grid.paint(canvas, Offset(plot.left - 8 - grid.width, y - grid.height / 2));
    }

    final n = buckets.length;
    Offset point(int i, double v) => Offset(
          plot.left + plot.width * (n == 1 ? 0.5 : i / (n - 1)),
          plot.bottom - plot.height * (v / maxV),
        );

    // 折线 + 数据点。
    for (var s = 0; s < series.length; s++) {
      final paint = Paint()
        ..color = colors[s]
        ..strokeWidth = 1.6
        ..style = PaintingStyle.stroke;
      final path = Path();
      for (var i = 0; i < n; i++) {
        final p = point(i, series[s](buckets[i]));
        if (i == 0) {
          path.moveTo(p.dx, p.dy);
        } else {
          path.lineTo(p.dx, p.dy);
        }
      }
      canvas.drawPath(path, paint);
      final dot = Paint()..color = colors[s];
      for (var i = 0; i < n; i++) {
        final p = point(i, series[s](buckets[i]));
        canvas.drawCircle(p, 2, dot);
      }
    }

    // 横轴标签抽稀：最多 8 个。
    final step = (n / 8).ceil();
    for (var i = 0; i < n; i += step) {
      final label = TextPainter(
        text: TextSpan(text: _bucketLabel(buckets[i].bucket), style: TextStyle(fontSize: 10, color: faint)),
        textDirection: TextDirection.ltr,
      )..layout();
      final x = point(i, 0).dx;
      label.paint(canvas, Offset((x - label.width / 2).clamp(plot.left - 20, plot.right - label.width), plot.bottom + 8));
    }
  }

  String _bucketLabel(DateTime b) {
    final l = b.toLocal();
    String two(int v) => v.toString().padLeft(2, '0');
    if (granularity == 'day') return '${two(l.month)}/${two(l.day)}';
    return '${two(l.month)}/${two(l.day)} ${two(l.hour)}:00';
  }

  @override
  bool shouldRepaint(_TrendPainter old) =>
      old.buckets != buckets || old.granularity != granularity;
}
