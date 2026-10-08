import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:msu_admin/theme.dart';
import 'package:msu_admin/ui/summary_band.dart';

/// 标签式搜索框:输入落词成标签、统计 chip 切换关键字、
/// ×/退格删除、外部 controller 种子文本标签化。
Future<void> pumpBand(
  WidgetTester tester,
  ValueChanged<String> onSearch, {
  bool clickableStats = false,
  TextEditingController? controller,
}) async {
  tester.view.physicalSize = const Size(1400, 1000);
  tester.view.devicePixelRatio = 1.0;
  addTearDown(tester.view.reset);
  await tester.pumpWidget(MaterialApp(
    theme: buildAppTheme(),
    home: Scaffold(
      body: SummaryBand(
        summary: '已配置 8 个模型',
        stats: const [
          BandStat(label: 'chat_completions', count: 5, colorKey: 'a'),
          BandStat(label: 'anthropic', count: 2, colorKey: 'b'),
        ],
        searchHint: '搜索模型标识、上游模型名或账号',
        clickableStats: clickableStats,
        controller: controller,
        onSearch: onSearch,
      ),
    ),
  ));
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('输入文本遇空格落成关键字标签并上报', (tester) async {
    final queries = <String>[];
    await pumpBand(tester, queries.add);

    await tester.enterText(find.byType(TextField), 'kimi ');
    await tester.pumpAndSettle();

    expect(queries, ['kimi']);
    expect(find.text('kimi'), findsOneWidget);
    expect(find.byIcon(Icons.close), findsOneWidget);
    // 输入框已清空,等待下一个关键字
    final field = tester.widget<TextField>(find.byType(TextField));
    expect(field.controller!.text, isEmpty);
  });

  testWidgets('回车落词;多关键字空格连接上报;重复关键字去重', (tester) async {
    final queries = <String>[];
    await pumpBand(tester, queries.add);

    await tester.enterText(find.byType(TextField), 'kimi');
    await tester.testTextInput.receiveAction(TextInputAction.done);
    await tester.pumpAndSettle();
    await tester.enterText(find.byType(TextField), 'K3 ');
    await tester.pumpAndSettle();
    await tester.enterText(find.byType(TextField), 'kimi ');
    await tester.pumpAndSettle();

    expect(queries, ['kimi', 'kimi K3']);
    expect(find.byIcon(Icons.close), findsNWidgets(2));
  });

  testWidgets('点标签 × 移出关键字', (tester) async {
    final queries = <String>[];
    await pumpBand(tester, queries.add);

    await tester.enterText(find.byType(TextField), 'kimi kiro ');
    await tester.pumpAndSettle();
    expect(queries, ['kimi kiro']);

    await tester.tap(find.text('kimi'));
    await tester.pumpAndSettle();
    expect(queries.last, 'kiro');
    expect(find.text('kimi'), findsNothing);
  });

  testWidgets('空输入退格删除最后一个关键字', (tester) async {
    final queries = <String>[];
    await pumpBand(tester, queries.add);

    await tester.enterText(find.byType(TextField), 'kimi kiro ');
    await tester.pumpAndSettle();

    await tester.sendKeyEvent(LogicalKeyboardKey.backspace);
    await tester.pumpAndSettle();
    expect(queries.last, 'kimi');
    await tester.sendKeyEvent(LogicalKeyboardKey.backspace);
    await tester.pumpAndSettle();
    expect(queries.last, '');
  });

  testWidgets('统计 chip 点击加入/移出关键字标签', (tester) async {
    final queries = <String>[];
    await pumpBand(tester, queries.add, clickableStats: true);

    await tester.tap(find.text('chat_completions: 5'));
    await tester.pumpAndSettle();
    expect(queries, ['chat_completions']);
    expect(find.text('chat_completions'), findsOneWidget);

    await tester.tap(find.text('anthropic: 2'));
    await tester.pumpAndSettle();
    expect(queries.last, 'chat_completions anthropic');

    await tester.tap(find.text('chat_completions: 5'));
    await tester.pumpAndSettle();
    expect(queries.last, 'anthropic');
  });

  testWidgets('外部 controller 种子文本落成关键字标签', (tester) async {
    final controller = TextEditingController();
    addTearDown(controller.dispose);
    final queries = <String>[];
    await pumpBand(tester, queries.add,
        clickableStats: true, controller: controller);

    controller.text = 'kimi-1';
    await tester.pumpAndSettle();

    expect(find.text('kimi-1'), findsOneWidget);
    // 与已有标签并存时整体替换(种子语义是完整改写)
    await tester.enterText(find.byType(TextField), 'chat_completions ');
    await tester.pumpAndSettle();
    expect(queries.last, 'kimi-1 chat_completions');
  });
}
