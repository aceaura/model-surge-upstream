import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:msu_admin/theme.dart';
import 'package:msu_admin/ui/styled_dropdown.dart';

void main() {
  testWidgets('dropdown trigger matches single-line TextFormField height',
      (tester) async {
    // 回归:下拉曾比输入框矮 5px(内容 19 vs 24),并排上下错位被两次点名。
    await tester.pumpWidget(MaterialApp(
      theme: buildAppTheme(),
      home: Scaffold(
        body: Column(children: [
          TextFormField(
            key: const ValueKey('tf'),
            decoration: const InputDecoration(border: OutlineInputBorder()),
          ),
          StyledDropdown(
            key: const ValueKey('dd'),
            value: 'a',
            options: const ['a', 'b'],
            onChanged: (_) {},
            decoration: const InputDecoration(border: OutlineInputBorder()),
          ),
        ]),
      ),
    ));

    final tf = tester.getSize(find.byKey(const ValueKey('tf')));
    final dd = tester.getSize(find.byKey(const ValueKey('dd')));
    expect(dd.height, tf.height,
        reason: '下拉触发器与单行输入框同高,并排才不错位');
  });
}
