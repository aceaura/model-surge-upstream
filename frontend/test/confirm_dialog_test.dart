import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

import 'package:msu_admin/theme.dart';
import 'package:msu_admin/ui/confirm_dialog.dart';

void main() {
  testWidgets('浅红头带:警告图标+深红左对齐标题,取消描边钮,确认红底危险钮',
      (tester) async {
    await tester.pumpWidget(MaterialApp(
      theme: buildAppTheme(),
      home: const Scaffold(
        body: ConfirmDialog(title: '删除会话', message: '不可恢复。'),
      ),
    ));

    final t = buildAppTheme().extension<AppTokens>()!;

    // 头带:dangerSoft 底 + 警告三角 + dangerInk 标题,不再用 DialogHeader 分隔线。
    expect(find.byIcon(Icons.warning_amber_rounded), findsOneWidget);
    expect(find.byType(Divider), findsNothing);
    final band = tester.widget<Container>(find.ancestor(
      of: find.byIcon(Icons.warning_amber_rounded),
      matching: find.byType(Container),
    ).first);
    expect(band.color, t.dangerSoft);
    final title = tester.widget<Text>(find.text('删除会话'));
    expect(title.style?.color, t.dangerInk);

    // 取消=描边钮;确认=FilledButton 且底色为主题 danger。
    expect(find.widgetWithText(OutlinedButton, '取消'), findsOneWidget);
    final confirm = find.widgetWithText(FilledButton, '删除');
    expect(confirm, findsOneWidget);
    final btn = tester.widget<FilledButton>(confirm);
    expect(btn.style?.backgroundColor?.resolve({}), t.danger);
  });

  testWidgets('取消回传 false,确认回传 true', (tester) async {
    bool? result;
    await tester.pumpWidget(MaterialApp(
      theme: buildAppTheme(),
      home: Scaffold(
        body: Builder(
          builder: (ctx) => TextButton(
            onPressed: () async {
              result = await showDialog<bool>(
                context: ctx,
                builder: (_) => const ConfirmDialog(
                  title: '删除会话',
                  message: '不可恢复。',
                ),
              );
            },
            child: const Text('打开'),
          ),
        ),
      ),
    ));

    await tester.tap(find.text('打开'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('取消'));
    await tester.pumpAndSettle();
    expect(result, isFalse);

    await tester.tap(find.text('打开'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('删除'));
    await tester.pumpAndSettle();
    expect(result, isTrue);
  });
}
