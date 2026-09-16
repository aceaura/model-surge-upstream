/// 自定义请求头的键值录入控件。
library;

import 'package:flutter/material.dart';

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

  @override
  Widget build(BuildContext context) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Row(
          children: [
            const Text('自定义请求头'),
            const Spacer(),
            IconButton(
              tooltip: '添加',
              icon: const Icon(Icons.add),
              onPressed: () => setState(() => _pairs.add(_Pair())),
            ),
          ],
        ),
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
    );
  }
}

class _Pair {
  _Pair({this.key = '', this.value = ''});
  String key;
  String value;
}
