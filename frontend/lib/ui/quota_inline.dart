import 'dart:async';
import 'dart:ui' show FontFeature;

import 'package:flutter/material.dart';

import '../api_client.dart';
import '../models.dart';
import '../theme.dart';

/// 行内额度摘要(CC Switch 式两行):列表加载后即向该账号的独立额度
/// 接口自动查询一次。第一行是相对查询时间+刷新钮,第二行是计量摘要
/// (percent 计量显示「标签: 已用%」并附重置倒计时)。点击刷新钮或
/// 摘要重查;autoIntervalMinutes > 0 时按间隔自动重查。
/// queryable 为 false(提供商未声明额度接口且未配置脚本)时不占位渲染。
class QuotaInline extends StatefulWidget {
  const QuotaInline({
    super.key,
    required this.client,
    required this.accountName,
    required this.queryable,
    this.autoIntervalMinutes = 0,
  });

  final ApiClient client;
  final String accountName;
  final bool queryable;

  /// 自动重查间隔(分钟),取自账号额度脚本配置;0 表示不自动刷新。
  final int autoIntervalMinutes;

  @override
  State<QuotaInline> createState() => _QuotaInlineState();
}

class _QuotaInlineState extends State<QuotaInline> {
  bool _busy = false;
  QuotaReport? _report;
  Object? _error;
  Timer? _autoTimer;

  /// 相对时间文案不走数据变化,靠低频 ticker 触发重排保持新鲜。
  Timer? _ticker;

  @override
  void initState() {
    super.initState();
    if (widget.queryable) {
      _query();
      _scheduleAuto();
    }
    _ticker = Timer.periodic(const Duration(seconds: 30), (_) {
      if (mounted) setState(() {});
    });
  }

  @override
  void dispose() {
    _autoTimer?.cancel();
    _ticker?.cancel();
    super.dispose();
  }

  void _scheduleAuto() {
    final m = widget.autoIntervalMinutes;
    if (m <= 0) return;
    _autoTimer = Timer.periodic(Duration(minutes: m), (_) => _query());
  }

  Future<void> _query() async {
    if (_busy) return;
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      final report = await widget.client.queryQuota(widget.accountName);
      if (!mounted) return;
      setState(() => _report = report);
    } catch (e) {
      if (!mounted) return;
      setState(() => _error = e);
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    if (!widget.queryable) return const SizedBox.shrink();
    final t = context.tokens;
    if (_error != null) {
      return _tappable(
          Text('额度不可用 · 点击重试', style: TextStyle(fontSize: 11.5, color: t.faint)));
    }
    final report = _report;
    if (report == null) {
      return Text(_busy ? '额度查询中…' : '',
          style: TextStyle(fontSize: 11.5, color: t.faint));
    }
    if (!report.queryable || report.meters.isEmpty) {
      return Text('额度未识别', style: TextStyle(fontSize: 11.5, color: t.faint));
    }
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      mainAxisSize: MainAxisSize.min,
      children: [
        _timeRow(t, report.at ?? DateTime.now()),
        const SizedBox(height: 2),
        _tappable(Text.rich(
          TextSpan(children: _meterSpans(t, report.meters)),
          style: TextStyle(fontSize: 12, color: t.dim, height: 1.2),
        )),
      ],
    );
  }

  /// 第一行:🕐 相对时间 + 行内刷新钮(查询中换成小 spinner),
  /// CC Switch 口径是 10px 浅灰小字。
  Widget _timeRow(AppTokens t, DateTime at) {
    return Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        Icon(Icons.schedule, size: 10, color: t.faint),
        const SizedBox(width: 3),
        Text(_relative(at), style: TextStyle(fontSize: 10, color: t.faint)),
        // CC Switch 的时间行是 gap-2 + p-1 按钮,刷新钮与文案之间
        // 留得开,不会贴住状态文字。
        const SizedBox(width: 10),
        if (_busy)
          SizedBox(
            width: 10,
            height: 10,
            child: CircularProgressIndicator(strokeWidth: 1.5, color: t.faint),
          )
        else
          InkWell(
            onTap: _query,
            borderRadius: BorderRadius.circular(8),
            child: Padding(
              padding: const EdgeInsets.all(2),
              child: Icon(Icons.refresh, size: 12, color: t.faint),
            ),
          ),
      ],
    );
  }

  /// 第二行计量摘要(CC Switch TierBadge 同款摆放):「标签:」浅灰 +
  /// 已用%按水位着色加粗(<70 绿 / 70-89 橙 / ≥90 红)+ 🕐倒计时
  /// 10px 浅灰;两条计量之间 8px 间隔。其余单位沿用余额/已用口径。
  List<InlineSpan> _meterSpans(AppTokens t, List<QuotaMeter> meters) {
    final spans = <InlineSpan>[];
    const gap = WidgetSpan(child: SizedBox(width: 8));
    for (final (i, m) in meters.take(2).indexed) {
      if (i > 0) spans.add(gap);
      if (m.unit == 'percent' && m.used != null) {
        spans.add(TextSpan(
            text: '${m.label?.isNotEmpty == true ? m.label : '额度'}:'));
        spans.add(TextSpan(
          text: m.amount(m.used),
          style: TextStyle(
            fontWeight: FontWeight.w600,
            color: _utilColor(t, m.used!),
            fontFeatures: const [FontFeature.tabularFigures()],
          ),
        ));
        final resetAt = m.resetAt;
        if (resetAt != null) {
          spans.add(const WidgetSpan(child: SizedBox(width: 3)));
          spans.add(WidgetSpan(
            alignment: PlaceholderAlignment.middle,
            child: Icon(Icons.schedule, size: 10, color: t.faint),
          ));
          spans.add(TextSpan(
            text: _countdown(resetAt),
            style: TextStyle(fontSize: 10, color: t.faint),
          ));
        }
      } else {
        spans.add(TextSpan(text: _compact(m)));
      }
    }
    return spans;
  }

  /// CC Switch utilizationColor 同款水位配色。
  static Color _utilColor(AppTokens t, double pct) {
    if (pct >= 90) return t.danger;
    if (pct >= 70) return t.warn;
    return t.success;
  }

  Widget _tappable(Widget child) => MouseRegion(
        cursor: SystemMouseCursors.click,
        child: GestureDetector(
          onTap: _busy ? null : _query,
          child: Tooltip(message: '点击重新查询额度', child: child),
        ),
      );

  /// 非 percent 计量的单行摘要:有余量看余量(预付费),否则看已用
  /// (后付费);最多取前两条计量,避免挤爆行宽。
  String _compact(QuotaMeter m) {
    if (m.remaining != null) return '余额 ${m.amount(m.remaining)}';
    if (m.used != null) return '已用 ${m.amount(m.used)}';
    if (m.total != null) return '总量 ${m.amount(m.total)}';
    return m.title;
  }

  /// CC Switch formatRelativeTime 同款口径。
  static String _relative(DateTime at) {
    final d = DateTime.now().difference(at);
    if (d.isNegative || d.inSeconds < 10) return '刚刚';
    if (d.inMinutes < 1) return '${d.inSeconds} 秒前';
    if (d.inHours < 1) return '${d.inMinutes} 分钟前';
    if (d.inDays < 1) return '${d.inHours} 小时前';
    return '${d.inDays} 天前';
  }

  /// 重置倒计时:⏱30m / ⏱3d8h;已过点显示「待重置」。
  static String _countdown(DateTime resetAt) {
    final d = resetAt.difference(DateTime.now());
    if (d.isNegative) return '待重置';
    if (d.inDays >= 1) return '${d.inDays}d${d.inHours % 24}h';
    if (d.inHours >= 1) return '${d.inHours}h${d.inMinutes % 60}m';
    return '${d.inMinutes}m';
  }
}
