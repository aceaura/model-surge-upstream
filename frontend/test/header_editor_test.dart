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
  testWidgets('空列表不渲染描边盒', (tester) async {
    await tester.pumpWidget(_app(const {}));
    expect(find.byType(TextFormField), findsNothing);
    expect(_box(find.byType(Form)), findsNothing);
  });

  testWidgets('点添加后键值行收进描边盒,删空后盒消失', (tester) async {
    await tester.pumpWidget(_app(const {}));

    await tester.tap(find.byTooltip('添加'));
    await tester.pump();

    final fields = find.byType(TextFormField);
    expect(fields, findsNWidgets(2));
    // 两个输入框都在同一个描边盒内,不再裸排在说明文字下。
    expect(_box(fields.first), findsOneWidget);
    expect(_box(fields.last), findsOneWidget);
    // 说明文字也收进描边盒(盒内顶部),不留在盒外上方。
    expect(_box(find.text('随每次请求发往上游，用于租户标识等附加头')),
        findsOneWidget);

    await tester.tap(find.byTooltip('删除'));
    await tester.pump();
    expect(find.byType(TextFormField), findsNothing);
    expect(_box(find.byType(Form)), findsNothing);
    // 盒消失后说明回到盒外渲染,不随盒消失。
    expect(find.text('随每次请求发往上游，用于租户标识等附加头'), findsOneWidget);
  });

  testWidgets('初始即带请求头时直接渲染在描边盒内', (tester) async {
    await tester.pumpWidget(_app(const {'X-Tenant': 'a-1'}));
    final fields = find.byType(TextFormField);
    expect(fields, findsNWidgets(2));
    expect(_box(fields.first), findsOneWidget);
  });
}
