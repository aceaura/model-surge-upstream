/// 参数 JSON 编辑控件。CC Switch 式分区排法:粗标题 + 灰说明在上,
/// 多行编辑框在下。内容不是合法 JSON object 时给出提示，
/// 由外层据此阻止提交。
library;

import 'dart:convert';

import 'package:flutter/material.dart';

import '../theme.dart';

/// parseJsonObject 返回 (对象, 错误说明)。空内容视为 {}。
(Map<String, dynamic>?, String?) parseJsonObject(String raw) {
  final text = raw.trim();
  if (text.isEmpty) return (<String, dynamic>{}, null);
  try {
    final decoded = jsonDecode(text);
    if (decoded is! Map<String, dynamic>) {
      return (null, '必须是 JSON 对象，例如 {"temperature":0.6}');
    }
    return (decoded, null);
  } on FormatException catch (e) {
    return (null, 'JSON 格式错误：${e.message}');
  }
}

String prettyJson(Map<String, dynamic> value) =>
    value.isEmpty ? '{}' : const JsonEncoder.withIndent('  ').convert(value);

class JsonField extends StatefulWidget {
  const JsonField({
    super.key,
    required this.label,
    required this.helper,
    required this.controller,
    required this.onValidityChanged,
  });

  final String label;
  final String helper;
  final TextEditingController controller;
  final ValueChanged<bool> onValidityChanged;

  @override
  State<JsonField> createState() => _JsonFieldState();
}

class _JsonFieldState extends State<JsonField> {
  String? _error;

  @override
  void initState() {
    super.initState();
    widget.controller.addListener(_validate);
    _validate();
  }

  @override
  void dispose() {
    widget.controller.removeListener(_validate);
    super.dispose();
  }

  void _validate() {
    final (_, error) = parseJsonObject(widget.controller.text);
    if (error != _error) {
      setState(() => _error = error);
      widget.onValidityChanged(error == null);
    }
  }

  /// 一键美化:合法 JSON 重排为 2 空格缩进,光标移到末尾;
  /// 非法时按钮禁用,不会走到这里。
  void _format() {
    final (value, error) = parseJsonObject(widget.controller.text);
    if (error != null || value == null) return;
    final formatted = prettyJson(value);
    if (formatted == widget.controller.text) return;
    widget.controller.value = TextEditingValue(
      text: formatted,
      selection: TextSelection.collapsed(offset: formatted.length),
    );
  }

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(
          widget.label,
          style: TextStyle(fontSize: 13.5, fontWeight: FontWeight.w600, color: t.ink),
        ),
        const SizedBox(height: 3),
        Text(widget.helper, style: TextStyle(fontSize: 12, color: t.faint)),
        const SizedBox(height: 10),
        Stack(
          children: [
            TextField(
              controller: widget.controller,
              maxLines: 5,
              minLines: 3,
              style: const TextStyle(fontFamily: 'Consolas'),
              decoration: InputDecoration(
                errorText: _error,
                border: const OutlineInputBorder(),
                // 与框架默认 (12,20,12,12) 一致,仅右侧预留格式化按钮的位置,
                // 避免长行文字压到图标
                contentPadding: const EdgeInsets.fromLTRB(12, 12, 44, 12),
              ),
            ),
            Positioned(
              top: 4,
              right: 4,
              child: IconButton(
                tooltip: _error == null ? '格式化 JSON' : 'JSON 非法，无法格式化',
                onPressed: _error == null ? _format : null,
                icon: const Icon(Icons.auto_fix_high, size: 18),
                visualDensity: VisualDensity.compact,
              ),
            ),
          ],
        ),
      ],
    );
  }
}
