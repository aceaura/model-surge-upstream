import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

import 'package:msu_admin/theme.dart';
import 'package:msu_admin/ui/header_editor.dart';

/// 包住键值行的描边盒:BoxDecoration 带 border 的 Container。
Finder _box(Finder descendant) => find.ancestor(
      of: descendant,
      matching: find.byWidgetPredicate(
        (w) =>
            w is Container &&
            w.decoration is BoxDecoration &&
            (w.decoration! as BoxDecoration).border != null,
      ),
    );

Widget _app(Map<String, String> initial) => MaterialApp(
      theme: buildAppTheme(),
      home: Scaffold(
        body: HeaderEditor(initial: initial, onChanged: (_) {}),
      ),
    );

void main() {
  testWidgets('空列表默认收起,说明作副标题、无描边盒', (tester) async {
    await tester.pumpWidget(_app(const {}));
    expect(find.byType(TextFormField), findsNothing);
    expect(find.text('随每次请求发往上游，用于租户标识等附加头'), findsOneWidget,
        reason: '说明收作分栏副标题');
    expect(_box(find.byType(Scaffold)), findsNothing);
  });

  testWidgets('展开后点添加,键值行收进描边盒;删空后盒消失', (tester) async {
    await tester.pumpWidget(_app(const {}));

    // 分栏默认收起,先点标题展开。
    await tester.tap(find.text('自定义请求头'));
    await tester.pumpAndSettle();

    await tester.tap(find.byTooltip('添加'));
    await tester.pumpAndSettle();

    final fields = find.byType(TextFormField);
    expect(fields, findsNWidgets(2));
    // 两个输入框都在同一个描边盒内。
    expect(_box(fields.first), findsOneWidget);
    expect(_box(fields.last), findsOneWidget);
    expect(find.text('已配置 1 个 · 随每次请求发往上游'), findsOneWidget,
        reason: '副标题改报已配置个数');

    await tester.tap(find.byTooltip('删除'));
    await tester.pumpAndSettle();
    expect(find.byType(TextFormField), findsNothing);
    expect(_box(find.byType(Scaffold)), findsNothing);
    expect(find.text('随每次请求发往上游，用于租户标识等附加头'), findsOneWidget,
        reason: '删空后副标题回到说明口径');
  });

  testWidgets('初始即带请求头时默认展开,直接渲染在描边盒内', (tester) async {
    await tester.pumpWidget(_app(const {'X-Tenant': 'a-1'}));
    final fields = find.byType(TextFormField);
    expect(fields, findsNWidgets(2));
    expect(_box(fields.first), findsOneWidget);
    expect(find.text('已配置 1 个 · 随每次请求发往上游'), findsOneWidget);
  });
}
