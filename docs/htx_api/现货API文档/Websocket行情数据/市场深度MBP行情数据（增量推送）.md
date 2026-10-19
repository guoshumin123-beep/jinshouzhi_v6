# market.$symbol.mbp.$levels (市场深度MBP行情数据（增量推送）)

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=7ec49b03-7773-11ed-9966-0242ac110003

## 接口信息
| 项目 | 内容 |
|---|---|
| 请求类型 | - |
| 是否验签 | 否 |
| 接口权限 | 读取 |
| 接口描述 | 用户可订阅此频道以接收最新深度行情MarketByPrice(MBP)的增量数据推送；同时，该频道支持用户以req方式请求获取全量数据。<br><br>同一个链接、同一个币种的前提下，假设既订阅了market.$symbol.mbp.$levels 又订阅了market.$symbol.mbp.refresh.$levels，那么会按照下面的顺序推送数据。<br><br>按照这种顺序推送：  增量 ----> 全量 <br><br>MBP增量推送及MBP全量REQ请求地址<br><br>wss://api.huobi.pro/feed<br><br>wss://api-aws.huobi.pro/feed<br><br>建议下游数据处理方式：<br>1）订阅增量数据并开始缓存；<br>2）请求全量数据（同等档位数）并根据该全量消息的seqNum与缓存增量数据中的prevSeqNum对齐；<br>3）开始连续增量数据接收与计算，构建并持续更新MBP订单簿；<br>4）每条增量数据的prevSeqNum须与前一条增量数据的seqNum一致，0则意味着存在增量数据丢失，须重新获取全量数据并对齐；<br>5）如果收到增量数据包含新增price档位，须将该price档位插入MBP订单簿中适当位置；<br>6）如果收到增量数据包含已有price档位，但size不同，须替换MBP订单簿中该price档位的size；<br>7）如果收到增量数据某price档位的size为0值，须将该price档位从MBP订单簿中删除；<br>8）如果收到单条增量数据中包含两个及以上price档位的更新，这些price档位须在MBP订单簿中被同时更新。<br><br>当前仅支持5档/20档MBP逐笔增量以及150档/400档MBP快照增量的推送，二者的区别为-<br>1）深度不同；<br>2）5档/20档为逐笔增量MBP行情，150档/400档为100毫秒定时快照增量MBP行情；<br>3）当5档/20档订单簿仅发生单边行情变化时，增量推送仅包含单边行情更新，比如，推送消息中包含数组asks，但不含数组bids；<br><br>当150档/400档订单簿仅发生单边行情变化时，增量推送包含双边行情更新，但其中一边行情为空，比如，推送消息中包含数组asks更新的同时，也包含bids空数组；<br><br>未来，150档/400档增量推送的数据行为将与5档/20档增量保持一致，即，单边深度行情变更时，推送消息中将不包含另一边行情深度行情；<br>4）当150档/400档订单簿在100毫秒时间间隔内未发生变化时，增量推送包含bids和asks空数组；<br><br>而5档/20档MBP逐笔增量，在订单簿未发生变化时，不推送数据；<br>未来，150档/400档增量推送的数据行为将与5档增量保持一致，即，在订单簿未发生变化时，不再推送空消息；<br>5）20档逐笔增量行情仅支持部分交易对:btcusdt,ethusdt,xrpusdt,eosusdt,ltcusdt,etcusdt,adausdt,dashusdt,bsvusdt,htusdt,dotusdt,linkusdt,iotausdt,zecusdt,trxusdt,xmrusdt,arusdt,dfausdt,nftusdt,uniusdt,dogeusdt,solusdt,xecusdt,lunausdt,bchusdt,maticusdt,vetusdt,xlmusdt,filusdt,thetausdt。150档/400档快照增量支持全部交易对。<br>6）所有交易对都支持5档增量推送。<br><br>REQ频道支持5档/20档/150档全量数据的获取。 |

## 请求地址
| 环境 | 地址 |
|---|---|
| 线上环境 | `wss://api.huobi.pro/feed` |
| 线上环境（aws客户首选） | `wss://api-aws.huobi.pro/feed` |

## 请求参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| symbol | Array | 是 | 交易代码（不支持通配符） |
| levels | Array | 是 | 深度档位（取值：5， 20， 150，400）<br>当前仅支持5档，20档，150或400档深度 |
| cid | string | 否 | 表示本次请求id |

## 请求示例
```json
{
  "sub": ["market.btcusdt.mbp.5","market.ethusdt.mbp.5","market.htxusdt.mbp.5"],
  "id": "id1"
}
```

## 响应示例
```json
{
"id":"id1",
"status":"ok",
"subbed":"market.btcusdt.mbp.5",
"ts":1489474081631
}
```

## 推送数据
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| seqNum | integer | 否 | 消息序列号 |
| prevSeqNum | integer | 否 | 上一消息序列号 |
| bids | object | 否 | 买盘，按price降序排列，["price","size"] |
| asks | object | 否 | 卖盘，按price升序排列，["price","size"] |

## 推送示例
```json
{ "ch":"market.btcusdt.mbp.5", "ts":1573199608679, "tick":{ "seqNum":100020146795, "prevSeqNum":100020146794, "asks":[ [645.140000000000000000,26.755973959140651643] ] } }
```

## 取消订阅示例
```json
{
  "unsub": "market.btcusdt.mbp.5",
  "id": "id1"
}
```

## SDK
- [JAVA SDK](https://github.com/HuobiRDCenter/huobi_Java)
- [PYTHON SDK](https://github.com/HuobiRDCenter/huobi_Python)
- [GO SDK](https://github.com/HuobiRDCenter/huobi_Golang)
- [C# SDK](https://github.com/HuobiRDCenter/huobi_CSharp)
- [C++ SDK](https://github.com/HuobiRDCenter/huobi_Cpp)
