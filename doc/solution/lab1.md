# **tinykv lab1 解题思路**

## Part 1 : Implement a standalone storage engine.

本部分是对底层badger api的包装，主要涉及修改的代码文件是standalone_storage.go,需要实现Storage接口的Write和Reader方法，来实现对底层badger数据库的读写。

### 1.Write部分实现思路

Write部分涉及到Put和Delete两种操作。

因为write_batch.go中已经实现了对badger中entry的put和delete操作，我们只需要判断batch中的每一个Modify的操作类型，然后直接调用write_batch.go中相对应的方法即可。

### 2.Reader部分实现思路

Reader部分会涉及到 point read和scan read两种不同读方式。

因为提示到应该使用badger.Txn来实现Reader函数，所以我们声明了一个badgerReader结构体来实现StorageReader接口，badgerReader结构体内部包含对badger.Txn的引用。

针对point read，
我们直接调用util.go中的GetCF等函数，对cf中指定key进行读取。

针对scan read，
直接调用cf_iterator.go中的NewCFIterator函数，返回一个迭代器，供part2中调用



## Part 2 : Implement raw key/value service handlers.

本部分需要实现RawGet/ RawScan/ RawPut/ RawDelete四个handlers，主要涉及修改的代码文件是raw_api.go

针对RawGet，
我们调用storage的Reader函数返回一个Reader，然后调用其GetCF函数进行点读取即可，读取之后需要判断对应key是否存在。

针对RawScan，
同样地调用storage的Reader函数返回一个Reader，然后调用其IterCF函数返回一个迭代器，然后使用迭代器读取即可。

针对RawPut和RawDelete，
声明对应的Modify后，调用storage.Write函数即可。