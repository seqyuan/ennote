# 技能

技能（Skill）是**给模型看的方法说明**：一段流程、一些约定、必要时附脚本。它不执行任何东西，
它让模型在合适的场合知道"该怎么做这件事"。

和工具的区别值得先分清：

| | 工具（Tool） | 技能（Skill） |
|---|---|---|
| 是什么 | 可执行的能力 | 可阅读的说明 |
| 谁执行 | Worker | 模型（读完自己照做） |
| 权限 | 受工具策略与审批约束 | 不是权限机制，只是文档 |

## 一个技能长什么样

最小形态就是**一个目录 + 一份 `SKILL.md`**：

```
skills/bio-workspace/
  SKILL.md          # 给模型读的正文
  skill.json        # 可选：显式声明元数据
  scripts/
    qc.sh
```

`skill.json` 可选字段：

```json
{
  "id": "bio-workspace",
  "version": "0.1.0",
  "description": "在本地生信工作区里跑质控与探索",
  "prompt": "SKILL.md",
  "allowedTools": ["read", "ls", "grep", "find", "bash"]
}
```

没有 `skill.json` 也完全可用（兼容 pi 生态的 `SKILL.md`-only 技能，Ennote 会自动合成清单）。

**`description` 一定要写清楚"什么时候该用"** —— 模型是凭它判断要不要读这份技能的。

## 模型是怎么看到技能的

每个 Run 启动时，Ennote 把技能目录整理成一份有界的清单（最多 16 KiB）注入系统提示：

```
- skill `bio-workspace`: 在本地生信工作区里跑质控与探索. Location: `/skills/bio-workspace/SKILL.md`
- category `scRNA`: 单细胞相关流程. Location: `/skills/scRNA/category.md`
```

然后：

1. 模型按需用 `read` 打开对应路径
2. `/skills` 在沙箱里是**只读挂载**，技能改不了它自己
3. 技能里写的相对路径，相对于该 `SKILL.md` 所在目录解析

由此可推出一个容易踩的坑：

!!! warning "技能依赖 `read` 工具"
    如果当前策略不允许 `read`，整个技能目录会被跳过（状态记为 disabled），模型一个字都看不到。
    在只读（`discuss`）模式下通常没问题；把工具收得太紧时要意识到这一点。

## 目录层级与覆盖优先级

技能可以分组：

```
skills/
  bio-workspace/
    SKILL.md
  scRNA/                 # 分类：需要 category.md
    category.md
    pseudotime/
      SKILL.md
```

同名冲突按来源优先级解析，**优先级数字小的赢**：

| 来源 | 位置 | 优先级 |
|---|---|---|
| user | `~/.ennote/skills/`（以及你在设置里加的自定义 root） | 0 |
| builtin | 随 npm 包发布的 `skills/` | 1 |

也就是说：想覆盖内置技能，在用户目录里放一个同名目录即可，不用改包里的东西。

## 三种装技能的方式

| 方式 | 怎么做 |
|---|---|
| 内置 | 随包发布，开箱可用 |
| 自己写 | 直接放进 `~/.ennote/skills/<name>/`，或让模型用 `create_skill` 工具当场生成 |
| 从市场装 | 设置 → 技能 → 搜索并安装；底层是 `npx skills add`，可选 global 或 project 作用域 |

从市场装的技能可以单独检查更新、更新、移除；自己写的技能不在这套管理里（它们就是你目录里的文件）。

!!! note "project 作用域需要先信任工作区"
    装到 Project 作用域要求该工作区已被信任。没信任时只能用 global。

## 启用与禁用

```bash
# 禁用某个技能（按相对路径）
PATCH /v1/skills/disabled/{relPath}
```

禁用的技能不出现在清单里，模型也就不会去读它。这比删掉更安全——文件还在，随时能开回来。

## 在 Role 和 Flow 里引用

| 场合 | 字段 | 语义 |
|---|---|---|
| Role | `skills.entries[]` | 每项 `{ skillId, mode }`，`mode` 为 `preload`（预载入）或 `available`（按需读） |
| Flow 的 Task | `skills: [name, ...]` | 该 Task 的子 Run 可用的技能 |

`preload` 适合那些"每次都必须先读"的硬性约定；`available` 适合"相关时才看"的流程。

## 技能的冻结点

一次 Run 用到的技能集合会随 Run 一起冻结：目录内容被物化到
`sessions/snapshots/skills/sha256-<digest>/`，并记录下每个技能的路径与摘要。

所以：**改技能不会改变已经开始的 Run**，下次 Run 才会用新内容。事后也能查到"这条回答是在哪版技能下产生的"。

## 写一个好技能的要点

1. **description 写触发条件**，不要写营销话术 —— 模型靠它决定读不读
2. **先写"不要用它做什么"** —— 边界比步骤更能减少误用
3. **相对路径**引用脚本与素材，不要写绝对路径
4. **别把权限写进技能** —— 技能不是策略，工具能不能用由权限模式与 Role 决定
5. 内容长了就分组，用 `category.md` + 子目录，让模型能逐层收敛

## 排障

| 现象 | 多半是 |
|---|---|
| 模型完全没提到某个技能 | 技能被禁用；或当前策略不允许 `read`，整个目录被跳过 |
| 模型不读你的技能 | `description` 没写清适用场景，或与已有技能重名被覆盖 |
| 改了技能但行为没变 | 改的是正在跑的 Run（已冻结），新开一个 Session / Run 才会生效 |
| 技能里的脚本找不到 | 用了绝对路径，或没按 `SKILL.md` 所在目录写相对路径 |
| 市场安装失败 | 需要网络；project 作用域还要求工作区已信任 |
