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

  testWidgets('fitContent trigger fits the longest option label without ellipsis',
      (tester) async {
    // 回归:fitContent 曾按 w600 无字距量文案、且少算 inputGap(2x4),
    // 日志页「全部来源/全部级别」这类与最长 ASCII 选项等宽的全 CJK
    // 文案在触发器里被截成「全部…」。
    const options = ['all', 'xy'];
    String label(String o) => o == 'all' ? '全部来源' : o;
    await tester.pumpWidget(MaterialApp(
      theme: buildAppTheme(),
      home: Scaffold(
        body: Align(
          alignment: Alignment.topLeft,
          child: StyledDropdown(
            value: 'all',
            options: options,
            labelOf: label,
            onChanged: (_) {},
            fitContent: true,
          ),
        ),
      ),
    ));

    final ctx = tester.element(find.text('全部来源'));
    final tw = tester.widget<Text>(find.text('全部来源'));
    final merged = DefaultTextStyle.of(ctx).style.merge(tw.style);
    // 每个选项文案的自然宽都必须不大于触发器文本槽,否则渲染即省略。
    final slot = tester.getSize(find.text('全部来源')).width;
    for (final o in options) {
      final natural = TextPainter(
        text: TextSpan(text: label(o), style: merged),
        maxLines: 1,
        textDirection: TextDirection.ltr,
        textScaler: MediaQuery.textScalerOf(ctx),
      )..layout();
      expect(natural.width, lessThanOrEqualTo(slot),
          reason: '选项「${label(o)}」自然宽须放得下,放不下即省略号');
      natural.dispose();
    }
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
