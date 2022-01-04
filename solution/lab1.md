<!-- TOC -->

- [解题思路](#解题思路)
    - [Part 1 : Implement a standalone storage engine.](#part-1--implement-a-standalone-storage-engine)
        - [1.Write 部分实现思路](#1write-部分实现思路)
        - [2.Reader 部分实现思路](#2reader-部分实现思路)
    - [Part 2 : Implement raw key/value service handlers.](#part-2--implement-raw-keyvalue-service-handlers)
- [相关知识学习](#相关知识学习)

<!-- /TOC -->

## 解题思路

### Part 1 : Implement a standalone storage engine.

本部分是对底层 badger api 的包装，主要涉及修改的代码文件是 standalone_storage.go, 需要实现 Storage 接口的 Write 和 Reader 方法，来实现对底层 badger 数据库的读写。

#### 1.Write 部分实现思路

Write 部分涉及到 Put 和 Delete 两种操作。

因为 write_batch.go 中已经实现了对 badger 中 entry 的 put 和 delete 操作，我们只需要判断 batch 中的每一个 Modify 的操作类型，然后直接调用 write_batch.go 中相对应的方法即可。

#### 2.Reader 部分实现思路

Reader 部分会涉及到 point read 和 scan read 两种不同读方式。

因为提示到应该使用 badger.Txn 来实现 Reader 函数，所以我们声明了一个 badgerReader 结构体来实现 StorageReader 接口，badgerReader 结构体内部包含对 badger.Txn 的引用。

针对 point read，
我们直接调用 util.go 中的 GetCF 等函数，对 cf 中指定 key 进行读取。

针对 scan read，
直接调用 cf_iterator.go 中的 NewCFIterator 函数，返回一个迭代器，供 part2 中调用。

### Part 2 : Implement raw key/value service handlers.

本部分需要实现 RawGet/ RawScan/ RawPut/ RawDelete 四个 handlers，主要涉及修改的代码文件是 raw_api.go。

针对 RawGet，
我们调用 storage 的 Reader 函数返回一个 Reader，然后调用其 GetCF 函数进行点读取即可，读取之后需要判断对应 key 是否存在。

针对 RawScan，
同样地调用 storage 的 Reader 函数返回一个 Reader，然后调用其 IterCF 函数返回一个迭代器，然后使用迭代器读取即可。

针对 RawPut 和 RawDelete，
声明对应的 Modify 后，调用 storage.Write 函数即可。

## 相关知识学习

LSM 是一个伴随 NoSQL 运动一起流行的存储引擎，相比 B+ 树以牺牲读性能的代价在写入性能上获得了较大的提升。

近年来，工业界和学术界均对 LSM 树进行了一定的研究，具体可以阅读 VLDB2018 有关 LSM 的综述：[LSM-based Storage Techniques: A Survey](https://arxiv.org/pdf/1812.07527.pdf), 也可直接阅读针对该论文我认为还不错的一篇 [中文概要总结](https://blog.shunzi.tech/post/vldbj-2018lsm-based-storage-techniques-a-survey/)。

介绍完了 LSM 综述，可以简单聊聊 badger，这是一个纯 go 实现的 LSM 存储引擎，参照了 FAST2016 有关 KV 分离 LSM 的设计： [WiscKey](https://www.usenix.org/system/files/conference/fast16/fast16-papers-lu.pdf) 。有关其项目的动机和一些 benchmark 结果可以参照其创始人的 [博客](https://dgraph.io/blog/post/badger/)。

对于 Wisckey 这篇论文，除了阅读论文以外，也可以参考此 [阅读笔记](https://www.scienjus.com/wisckey/) 和此 [总结博客](https://www.skyzh.dev/posts/articles/2021-08-07-lsm-kv-separation-overview/)。这两篇资料较为系统地介绍了现在学术界和工业界对于 KV 分离 LSM 的一些设计和实现。

实际上对于目前的 NewSQL 数据库，其底层大多数都是一个分布式 KV 存储系统。对于 OLAP 业务，其往往采用行存的方式，即 key 对应的 value 便是一个 tuple。在这样的架构下，value 往往很大，因而采用 KV 分离的设计往往能够减少大量的写放大，从而提升性能。

之前和腾讯云的一个大佬聊过，他有说 TiKV 的社区版和商业版存储引擎性能差异很大。目前想一下，KV 分离可能便是 RocksDB 和 Titan 的最大区别吧。