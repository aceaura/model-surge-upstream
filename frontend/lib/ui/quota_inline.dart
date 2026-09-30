import 'package:flutter/material.dart';

import '../api_client.dart';
import '../models.dart';
import '../theme.dart';

/// 行内额度摘要(CC Switch 式):列表加载后即向该账号的独立额度
/// 接口自动查询一次,以轻量文字嵌在配置行右侧;点击可重新查询。
/// queryable 为 false(提供商未声明额度接口)时不占位渲染。
class QuotaInline extends StatefulWidget {
  const QuotaInline({
    super.key,
    required this.client,
    required this.accountName,
    required this.queryable,
  });

  final ApiClient client;
  final String accountName;
  final bool queryable;

  @override
  State<QuotaInline> createState() => _QuotaInlineState();
}

class _QuotaInlineState extends State<QuotaInline> {
  bool _busy = false;
  QuotaReport? _report;
  Object? _error;

  @override
  void initState() {
    super.initState();
    if (widget.queryable) _query();
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
    final text = report.meters.take(2).map(_compact).join('   ');
    return _tappable(Text(text, style: TextStyle(fontSize: 12, color: t.dim)));
  }

  Widget _tappable(Widget child) => MouseRegion(
        cursor: SystemMouseCursors.click,
        child: GestureDetector(
          onTap: _busy ? null : _query,
          child: Tooltip(message: '点击重新查询额度', child: child),
        ),
      );

  /// 单行摘要:有余量看余量(预付费),否则看已用(后付费),
  /// 最多取前两条计量,避免挤爆行宽。
  String _compact(QuotaMeter m) {
    if (m.remaining != null) return '余额 ${m.amount(m.remaining)}';
    if (m.used != null) return '已用 ${m.amount(m.used)}';
    if (m.total != null) return '总量 ${m.amount(m.total)}';
    return m.title;
  }
}
