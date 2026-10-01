/// 自定义请求头的键值录入控件,收进 CC Switch 式可折叠分栏(与额度脚本
/// 同款):收起时一行标题+状态,展开后才是描边盒键值行与「添加」钮。
library;

import 'package:flutter/material.dart';

import '../theme.dart';
import 'collapsible_section.dart';

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

  static const _desc = '随每次请求发往上游，用于租户标识等附加头';

  String get _subtitle => _pairs.isNotEmpty
      ? '已配置 ${_pairs.length} 个 · 随每次请求发往上游'
      : _desc;

  void _emit() {
    final out = <String, String>{};
    for (final p in _pairs) {
      final k = p.key.trim();
      if (k.isNotEmpty) out[k] = p.value;
    }
    widget.onChanged(out);
  }

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    return CollapsibleSection(
      icon: Icons.tune_outlined,
      title: '自定义请求头',
      subtitle: _subtitle,
      initiallyExpanded: _pairs.isNotEmpty,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          if (_pairs.isNotEmpty)
            Container(
              decoration: BoxDecoration(
                border: Border.all(color: t.border),
                borderRadius: BorderRadius.circular(AppConst.radiusCard),
              ),
              padding: const EdgeInsets.fromLTRB(12, 12, 12, 4),
              child: Column(
                children: [
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
          Align(
            alignment: Alignment.centerLeft,
            child: Tooltip(
              message: '添加',
              child: TextButton.icon(
                onPressed: () => setState(() => _pairs.add(_Pair())),
                icon: const Icon(Icons.add, size: 16),
                label: const Text('添加'),
              ),
            ),
          ),
        ],
      ),
    );
  }
}

class _Pair {
  _Pair({this.key = '', this.value = ''});
  String key;
  String value;
}
