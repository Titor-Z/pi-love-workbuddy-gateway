// 资源导入的类型声明。
//
// Vite 会在构建时把这些变成可用的 URL，但 TypeScript 不知道有这回事，
// 于是 svelte-check 把它们报成「找不到模块」。这里只补类型、不改行为。
declare module "*.png" {
  const src: string;
  export default src;
}
declare module "*.css";
