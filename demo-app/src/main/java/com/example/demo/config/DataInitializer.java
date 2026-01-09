// Copyright 2023 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package com.example.demo.config;

import com.example.demo.db.Book;
import com.example.demo.db.BookRepository;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.boot.CommandLineRunner;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;

@Configuration
public class DataInitializer {
  private static final Logger logger = LoggerFactory.getLogger(DataInitializer.class);

  @Bean
  CommandLineRunner initDatabase(BookRepository repository) {
    return args -> {
      // Check if database is already populated
      long count = repository.count();
      if (count > 0) {
        logger.info("Database already contains {} books, skipping initialization", count);
        return;
      }

      logger.info("Initializing database with sample books...");
      
      // Create and save books
      Book book1 = new Book("Ubik", "Philip K. Dick", "Science Fiction");
      repository.save(book1);
      logger.info("Created book: {}", book1.getTitle());

      Book book2 = new Book("Enders game", "Orson Scott Card", "Science Fiction");
      repository.save(book2);
      logger.info("Created book: {}", book2.getTitle());

      logger.info("Database initialization completed. Total books: {}", repository.count());
    };
  }
}
