# /v2/user/uid (母子用户获取用户UID)

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=7ec491c9-7773-11ed-9966-0242ac110003

## 接口信息
| 项目 | 内容 |
|---|---|
| 请求类型 | GET |
| 是否验签 | 是 |
| 接口权限 | 读取 |
| 接口描述 | 此接口用于母子用户查询本用户UID |

## 请求地址
| 环境 | 地址 |
|---|---|
| 线上环境 | `https://api.huobi.pro` |
| 线上环境（aws客户首选） | `https://api-aws.huobi.pro` |

**请求参数说明**：<br/>此接口不接受任何参数。

## 响应参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| code | int | 是 | 状态码 |
| message | string | 否 | 错误描述（如有） |
| data | long | 是 | 用户UID |

## 请求示例
```http
curl"https://api.huobi.pro/v2/user/uid"
```

## 响应示例
```json
{
"code":200,
"data":63628520
}
```

## SDK
- [JAVA SDK](https://github.com/HuobiRDCenter/huobi_Java)
- [PYTHON SDK](https://github.com/HuobiRDCenter/huobi_Python)
- [GO SDK](https://github.com/HuobiRDCenter/huobi_Golang)
- [C# SDK](https://github.com/HuobiRDCenter/huobi_CSharp)
- [C++ SDK](https://github.com/HuobiRDCenter/huobi_Cpp)
