import 'package:flutter/material.dart';

import '../api_client.dart';
import '../models.dart';
import '../ui/feedback.dart';

/// 额度视图：手动触发查询。provider 不支持时显示"不可查询"而非错误。
class QuotaDialog extends StatefulWidget {
  const QuotaDialog({
    super.key,
    required this.client,
    required this.account,
  });

  final ApiClient client;
  final Account account;

  @override
  State<QuotaDialog> createState() => _QuotaDialogState();
}

class _QuotaDialogState extends State<QuotaDialog> {
  bool _busy = false;
  QuotaReport? _report;
  Object? _error;

  Future<void> _query() async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      final report = await widget.client.queryQuota(widget.account.name);
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
    return AlertDialog(
      title: Text('账号额度 ${widget.account.name}'),
      content: SizedBox(width: 420, child: _body(context)),
      actions: [
        TextButton(
          onPressed: () => Navigator.of(context).pop(),
          child: const Text('关闭'),
        ),
        BusyButton(
          busy: _busy,
          onPressed: _query,
          child: Text(_report == null && _error == null ? '查询额度' : '重新查询'),
        ),
      ],
    );
  }

  Widget _body(BuildContext context) {
    if (_error != null) {
      return SelectableText(
        describeError(_error!),
        style: TextStyle(color: Theme.of(context).colorScheme.error),
      );
    }
    final report = _report;
    if (report == null) {
      return const Text('点击「查询额度」向上游发起一次查询。');
    }
    if (!report.queryable) {
      return const Text('该提供商不支持额度查询。');
    }
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      mainAxisSize: MainAxisSize.min,
      children: [
        _row('余量', _amount(report.remaining, report.currency)),
        _row('总量', _amount(report.total, report.currency)),
        _row('重置规律', report.reset ?? '未声明'),
      ],
    );
  }

  String _amount(double? value, String? currency) {
    if (value == null) return '上游未提供';
    return currency == null || currency.isEmpty
        ? '$value'
        : '$value $currency';
  }

  Widget _row(String label, String value) => Padding(
        padding: const EdgeInsets.symmetric(vertical: 4),
        child: Row(
          children: [
            SizedBox(width: 88, child: Text(label)),
            Expanded(child: SelectableText(value)),
          ],
        ),
      );
}
