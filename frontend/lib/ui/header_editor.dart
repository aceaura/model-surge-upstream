/// 自定义请求头的键值录入控件。分区标题与「添加」钮走 FormSection 式排法。
library;

import 'package:flutter/material.dart';

import '../theme.dart';
import 'form_page.dart';

class HeaderEditor extends StatefulWidget {
  const HeaderEditor({
    super.key,
    required this.initial,
    required this.onChanged,
  });

  final Map<String, String> initial;
  final ValueChanged<Map<String, String>> onChanged;

  @override
  State<HeaderEditor> createState() => _HeaderEditorState();
}

class _HeaderEditorState extends State<HeaderEditor> {
  late final List<_Pair> _pairs = widget.initial.entries
      .map((e) => _Pair(key: e.key, value: e.value))
      .toList();

  void _emit() {
    final out = <String, String>{};
    for (final p in _pairs) {
      final k = p.key.trim();
      if (k.isNotEmpty) out[k] = p.value;
    }
    widget.onChanged(out);
  }

  static const _desc = '随每次请求发往上游，用于租户标识等附加头';

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    return FormSection(
      title: '自定义请求头',
      trailing: IconButton(
        tooltip: '添加',
        icon: const Icon(Icons.add, size: 18),
        visualDensity: VisualDensity.compact,
        onPressed: () => setState(() => _pairs.add(_Pair())),
      ),
      // 描边盒常驻:说明文字固定在盒内顶部,键值行追加在其下;
      // 空列表时盒内只有说明,不再盒内盒外两副面孔。
      child: Container(
        // 空列表时盒内只有短说明,也要撑满整行宽度。
        width: double.infinity,
        decoration: BoxDecoration(
          border: Border.all(color: t.border),
          borderRadius: BorderRadius.circular(AppConst.radiusCard),
        ),
        // 有行时末行自带 8px 底距,盒底只补 4;空盒则上下对称。
        padding: EdgeInsets.fromLTRB(12, 12, 12, _pairs.isEmpty ? 12 : 4),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(_desc, style: TextStyle(fontSize: 12, color: t.faint)),
            if (_pairs.isNotEmpty) const SizedBox(height: 10),
            for (var i = 0; i < _pairs.length; i++)
              Padding(
                padding: const EdgeInsets.only(bottom: 8),
                child: Row(
                  children: [
                    Expanded(
                      child: TextFormField(
                        initialValue: _pairs[i].key,
                        decoration: const InputDecoration(
                          labelText: '名',
                          border: OutlineInputBorder(),
                          isDense: true,
                        ),
                        onChanged: (v) {
                          _pairs[i].key = v;
                          _emit();
                        },
                      ),
                    ),
                    const SizedBox(width: 8),
                    Expanded(
                      flex: 2,
                      child: TextFormField(
                        initialValue: _pairs[i].value,
                        decoration: const InputDecoration(
                          labelText: '值',
                          border: OutlineInputBorder(),
                          isDense: true,
                        ),
                        onChanged: (v) {
                          _pairs[i].value = v;
                          _emit();
                        },
                      ),
                    ),
                    IconButton(
                      tooltip: '删除',
                      icon: const Icon(Icons.remove_circle_outline),
                      onPressed: () {
                        setState(() => _pairs.removeAt(i));
                        _emit();
                      },
                    ),
                  ],
                ),
              ),
          ],
        ),
      ),
    );
  }
}

class _Pair {
  _Pair({this.key = '', this.value = ''});
  String key;
  String value;
}
