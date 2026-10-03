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

  testWidgets('menu widens to fit long options instead of ellipsizing them',
      (tester) async {
    // 对话页模型选择器:200px 触发器配 codex-1/gpt-6.1-sol 这类长选项,
    // 菜单须比触发器宽、选项完整显示(用户点名「选框模型名补全」)。
    await tester.pumpWidget(MaterialApp(
      theme: buildAppTheme(),
      home: Scaffold(
        body: Align(
          alignment: Alignment.topLeft,
          child: SizedBox(
            width: 200,
            child: StyledDropdown(
              value: 'codex-1/gpt-6.1-sol',
              options: const ['codex-1/gpt-6.1-sol', 'kimi-1/k3-256k'],
              onChanged: (_) {},
            ),
          ),
        ),
      ),
    ));

    await tester.tap(find.byType(StyledDropdown));
    await tester.pumpAndSettle();

    final item = find.ancestor(
        of: find.text('codex-1/gpt-6.1-sol').last,
        matching: find.byType(InkResponse));
    final w = tester.getSize(item).width;
    expect(w, greaterThan(200), reason: '菜单按选项自然宽加宽,不钉触发器宽');
    // 自然宽上限:文本宽 + 行 padding 20 + check 位 24 + 菜单 padding 12。
    expect(w, lessThan(320), reason: '加宽到刚好放下,不无限制撑开');
  });
}
