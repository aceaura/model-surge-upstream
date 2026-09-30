import 'package:flutter/material.dart';

/// 非颜色常量(不随明暗变化)。
abstract final class AppConst {
  static const radiusCard = 16.0;
  static const radiusCtrl = 10.0;
  // 字体栈对齐 CC Switch 的 system-ui:拉丁/数字用 Segoe UI,
  // 中文经 fallback 落到雅黑(单设雅黑会让拉丁字形发闷)
  static const fontFamily = 'Segoe UI';
  static const fontFallback = ['Microsoft YaHei'];
  static const fontMono = 'Cascadia Code';
}

/// 主题色板(ThemeExtension):浅色/深色两套,组件经 context.tokens 取色。
/// 浅色向 CC Switch 靠拢:近白底、柔和边框、翡翠绿主色。
class AppTokens extends ThemeExtension<AppTokens> {
  final Color bg, surface, border, ink, dim, faint;
  final Color primary, primaryInk, primarySoft;
  final Color success, successSoft, warn, danger, dangerSoft;
  final Color violet;

  const AppTokens({
    required this.bg,
    required this.surface,
    required this.border,
    required this.ink,
    required this.dim,
    required this.faint,
    required this.primary,
    required this.primaryInk,
    required this.primarySoft,
    required this.success,
    required this.successSoft,
    required this.warn,
    required this.danger,
    required this.dangerSoft,
    required this.violet,
  });

  /// 浅色(CC Switch 式:近白灰底、白卡、浅边框、翡翠绿)。
  static const light = AppTokens(
    bg: Color(0xFFF5F6F8),
    surface: Color(0xFFFFFFFF),
    border: Color(0xFFE4E7ED),
    ink: Color(0xFF171B24),
    dim: Color(0xFF57606E),
    faint: Color(0xFF7A8494),
    primary: Color(0xFF16A34A),
    primaryInk: Color(0xFF15803D),
    primarySoft: Color(0xFFDDF2E4),
    success: Color(0xFF1E9E62),
    successSoft: Color(0xFFE4F6EC),
    warn: Color(0xFFC98A0B),
    danger: Color(0xFFC03535),
    dangerSoft: Color(0xFFFBEAEA),
    violet: Color(0xFF7A5AF8),
  );

  /// 深色(跟随系统)。
  static const dark = AppTokens(
    bg: Color(0xFF0F141C),
    surface: Color(0xFF161D29),
    border: Color(0xFF263040),
    ink: Color(0xFFE9EDF5),
    dim: Color(0xFFB4BDCC),
    faint: Color(0xFF98A2B6),
    primary: Color(0xFF34C77B),
    primaryInk: Color(0xFF9BE7C2),
    primarySoft: Color(0xFF173627),
    success: Color(0xFF3FBF7F),
    successSoft: Color(0xFF15362B),
    warn: Color(0xFFE0A83C),
    danger: Color(0xFFE26868),
    dangerSoft: Color(0xFF3D2020),
    violet: Color(0xFF9E8CFC),
  );

  @override
  AppTokens copyWith() => this; // 不可变,不支持部分覆盖

  @override
  AppTokens lerp(AppTokens? other, double t) => t < 0.5 ? this : other!;
}

extension AppTokensX on BuildContext {
  AppTokens get tokens => Theme.of(this).extension<AppTokens>()!;
}

ThemeData _build(AppTokens t, Brightness brightness) {
  final isDark = brightness == Brightness.dark;
  final base = isDark
      ? ThemeData.dark(useMaterial3: true)
      : ThemeData.light(useMaterial3: true);
  final scheme = (isDark ? ColorScheme.dark : ColorScheme.light)(
    primary: t.primary,
    onPrimary: Colors.white,
    primaryContainer: t.primarySoft,
    onPrimaryContainer: t.primaryInk,
    surface: t.bg,
    onSurface: t.ink,
    surfaceContainerHighest: t.surface,
    outline: t.border,
    outlineVariant: t.border,
    error: t.danger,
  );
  return base.copyWith(
    colorScheme: scheme,
    scaffoldBackgroundColor: t.bg,
    textTheme: base.textTheme.apply(
        fontFamily: AppConst.fontFamily,
        fontFamilyFallback: AppConst.fontFallback),
    extensions: [t],
    cardTheme: CardThemeData(
      color: t.surface,
      elevation: 0.4,
      shadowColor: isDark ? Colors.transparent : const Color(0x0A171B24),
      margin: EdgeInsets.zero,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(AppConst.radiusCard),
        side: BorderSide(color: t.border),
      ),
    ),
    appBarTheme: AppBarTheme(
      backgroundColor: t.surface,
      foregroundColor: t.ink,
      elevation: 0,
      scrolledUnderElevation: 0,
      centerTitle: false,
      titleTextStyle: TextStyle(
        fontSize: 15,
        fontWeight: FontWeight.w700,
        color: t.ink,
        fontFamily: AppConst.fontFamily,
        fontFamilyFallback: AppConst.fontFallback,
      ),
    ),
    chipTheme: base.chipTheme.copyWith(
      backgroundColor: t.bg,
      side: BorderSide(color: t.border),
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(7)),
      labelStyle: TextStyle(
        fontSize: 11.5,
        color: t.dim,
        fontFamily: AppConst.fontFamily,
        fontFamilyFallback: AppConst.fontFallback,
      ),
      padding: const EdgeInsets.symmetric(horizontal: 6),
    ),
    dialogTheme: DialogThemeData(
      backgroundColor: t.surface,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(AppConst.radiusCard),
      ),
    ),
    filledButtonTheme: FilledButtonThemeData(
      style: FilledButton.styleFrom(
        backgroundColor: t.primary,
        foregroundColor: Colors.white,
        // 显式钉字体族:按钮 textStyle 若缺 fontFamily,合并链上会丢掉
        // textTheme 的族设置回退 Roboto(中文 tofu)
        textStyle: const TextStyle(
            fontSize: 13,
            fontWeight: FontWeight.w600,
            fontFamily: AppConst.fontFamily,
            fontFamilyFallback: AppConst.fontFallback),
        padding: const EdgeInsets.symmetric(horizontal: 18, vertical: 12),
        shape: RoundedRectangleBorder(
          borderRadius: BorderRadius.circular(AppConst.radiusCtrl),
        ),
      ),
    ),
    outlinedButtonTheme: OutlinedButtonThemeData(
      style: OutlinedButton.styleFrom(
        foregroundColor: t.dim,
        textStyle: const TextStyle(
            fontSize: 13,
            fontWeight: FontWeight.w600,
            fontFamily: AppConst.fontFamily,
            fontFamilyFallback: AppConst.fontFallback),
        padding: const EdgeInsets.symmetric(horizontal: 18, vertical: 12),
        side: BorderSide(color: t.border),
        shape: RoundedRectangleBorder(
          borderRadius: BorderRadius.circular(AppConst.radiusCtrl),
        ),
      ),
    ),
    inputDecorationTheme: InputDecorationTheme(
      isDense: true,
      filled: true,
      fillColor: t.surface,
      contentPadding: const EdgeInsets.symmetric(horizontal: 13, vertical: 11),
      border: OutlineInputBorder(
        borderRadius: BorderRadius.circular(AppConst.radiusCtrl),
        borderSide: BorderSide(color: t.border),
      ),
      enabledBorder: OutlineInputBorder(
        borderRadius: BorderRadius.circular(AppConst.radiusCtrl),
        borderSide: BorderSide(color: t.border),
      ),
      focusedBorder: OutlineInputBorder(
        borderRadius: BorderRadius.circular(AppConst.radiusCtrl),
        borderSide: BorderSide(color: t.primary, width: 1.5),
      ),
      errorBorder: OutlineInputBorder(
        borderRadius: BorderRadius.circular(AppConst.radiusCtrl),
        borderSide: BorderSide(color: t.danger),
      ),
      hintStyle: TextStyle(fontSize: 12, color: t.faint),
    ),
    dividerTheme: DividerThemeData(color: t.border, thickness: 1, space: 1),
    snackBarTheme: SnackBarThemeData(
      behavior: SnackBarBehavior.floating,
      backgroundColor: isDark ? const Color(0xFF263040) : null,
      contentTextStyle: TextStyle(
          color: isDark ? t.ink : null,
          fontFamily: AppConst.fontFamily,
          fontFamilyFallback: AppConst.fontFallback),
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(10)),
    ),
    progressIndicatorTheme: ProgressIndicatorThemeData(
      linearTrackColor:
          isDark ? const Color(0xFF263040) : const Color(0xFFC9D0DD),
    ),
  );
}

ThemeData buildAppTheme() => _build(AppTokens.light, Brightness.light);
ThemeData buildAppDarkTheme() => _build(AppTokens.dark, Brightness.dark);
